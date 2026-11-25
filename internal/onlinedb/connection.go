// Package onlinedb connects to SQL servers and reads their catalogues and rows.
package onlinedb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"excellens/internal/formats"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"
	goora "github.com/sijms/go-ora/v2"

	"golang.org/x/net/idna"
)

const connectTimeout = 12 * time.Second

type Protocol struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	PlainPort int    `json:"plainPort"`
	TLSPort   int    `json:"tlsPort"`
}

type Engine struct {
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	Protocols []Protocol `json:"protocols"`
}

func Engines() []Engine {
	return []Engine{
		{"mysql", "MySQL / MariaDB", []Protocol{{"native", "MySQL", 3306, 3306}}},
		{"postgres", "PostgreSQL", []Protocol{{"native", "PostgreSQL", 5432, 5432}}},
		{"oracle", "Oracle", []Protocol{{"service", "Oracle 服务名", 1521, 2484}, {"sid", "Oracle SID", 1521, 2484}}},
		{"sqlserver", "SQL Server", []Protocol{{"native", "TDS", 1433, 1433}}},
		{"clickhouse", "ClickHouse", []Protocol{{"native", "Native", 9000, 9440}, {"http", "HTTP / HTTPS", 8123, 8443}}},
	}
}

type Config struct {
	Engine   string `json:"engine"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
	User     string `json:"user"`
	Password string `json:"password"`
	TLS      string `json:"tls"`
	CA       string `json:"ca"`
}

type Source struct {
	Engine, Protocol, Host, Database, TLS string
	Port                                  int
}

type Connection struct {
	db           *sql.DB
	engine       Engine
	source       Source
	database     string
	schema       string
	withDatabase bool
}

func (c *Connection) Close() error { return c.db.Close() }

func (c *Connection) Source() Source { return c.source }

func (c *Connection) Label() string {
	if c.database == "" {
		return c.engine.Label
	}
	return c.engine.Label + " · " + c.database
}

func Open(ctx context.Context, cfg Config) (*Connection, error) {
	engine, proto, err := configure(&cfg)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := databaseTLS(cfg)
	if err != nil {
		return nil, err
	}
	database := cfg.Database
	if database == "" && engine.ID == "postgres" {
		database = "postgres"
	}
	conn := &Connection{engine: engine, database: database, schema: cfg.Schema, withDatabase: cfg.Database != "",
		source: Source{Engine: engine.ID, Protocol: proto.ID, Host: cfg.Host, Port: cfg.Port, Database: database, TLS: cfg.TLS}}
	conn.db, err = openDriver(cfg, proto, tlsConfig)
	if err != nil {
		return nil, databaseError(err, "无法初始化数据库连接")
	}
	conn.db.SetMaxOpenConns(1)
	conn.db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err = conn.db.PingContext(conn.readContext(ctx)); err != nil {
		conn.Close()
		return nil, databaseError(err, "登录失败，请核对连接参数和账户权限")
	}
	return conn, nil
}

// ClickHouse turns a reported deadline into a server max_execution_time
// setting, which accounts in readonly mode cannot change. Done and Err
// still enforce the parent's timeout and cancellation on the client.
type clickHouseQueryContext struct{ context.Context }

func (clickHouseQueryContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func (c *Connection) readContext(ctx context.Context) context.Context {
	if c.engine.ID == "clickhouse" {
		return clickHouseQueryContext{ctx}
	}
	return ctx
}

func configure(cfg *Config) (Engine, Protocol, error) {
	var engine Engine
	for _, candidate := range Engines() {
		if candidate.ID == cfg.Engine {
			engine = candidate
			break
		}
	}
	if engine.ID == "" {
		return Engine{}, Protocol{}, errors.New("请选择数据库类型")
	}
	var proto Protocol
	for _, candidate := range engine.Protocols {
		if candidate.ID == cfg.Protocol || cfg.Protocol == "" {
			proto = candidate
			break
		}
	}
	if proto.ID == "" {
		return engine, proto, errors.New("当前数据库不提供该协议")
	}
	cfg.Host = strings.TrimSpace(cfg.Host)
	if strings.HasPrefix(cfg.Host, "[") && strings.HasSuffix(cfg.Host, "]") {
		cfg.Host = cfg.Host[1 : len(cfg.Host)-1]
	}
	host, err := databaseHost(cfg.Host)
	if err != nil {
		return engine, proto, err
	}
	cfg.Host = host
	cfg.Database, cfg.Schema = strings.TrimSpace(cfg.Database), strings.TrimSpace(cfg.Schema)
	if cfg.User == "" || len(cfg.User) > 512 || len(cfg.Database) > 384 || len(cfg.Schema) > 512 || len(cfg.Password) > 4096 ||
		strings.ContainsFunc(cfg.User+cfg.Database+cfg.Schema, unicode.IsControl) {
		return engine, proto, errors.New("请填写有效的用户名、数据库和架构")
	}
	if engine.ID == "oracle" && (cfg.Database == "" || strings.ContainsAny(cfg.Database, "/=?&(),\\")) {
		return engine, proto, errors.New("请输入 Oracle 服务名或 SID")
	}
	if cfg.TLS == "" {
		cfg.TLS = "verify"
	}
	if cfg.TLS != "verify" && cfg.TLS != "plain" {
		return engine, proto, errors.New("请选择 TLS 或普通 TCP")
	}
	if cfg.Port == 0 {
		cfg.Port = proto.PlainPort
		if cfg.TLS == "verify" {
			cfg.Port = proto.TLSPort
		}
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return engine, proto, errors.New("端口须在 1 至 65535 之间")
	}
	return engine, proto, nil
}

func databaseHost(host string) (string, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.String(), nil
	}
	if len(host) <= 768 && host != "" && !strings.ContainsAny(host, "/:?@#%\\[] ") {
		if ascii, err := idna.Lookup.ToASCII(host); err == nil && len(ascii) <= 253 {
			for _, label := range strings.Split(strings.TrimSuffix(ascii, "."), ".") {
				if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
					return "", errors.New("请输入有效的域名或 IP 地址")
				}
			}
			return ascii, nil
		}
	}
	return "", errors.New("请输入有效的域名或 IP 地址，端口请单独填写")
}

func databaseTLS(cfg Config) (*tls.Config, error) {
	if cfg.TLS == "plain" {
		if cfg.CA != "" {
			return nil, errors.New("自定义 CA 证书需要使用 TLS")
		}
		return nil, nil
	}
	host := strings.TrimSuffix(cfg.Host, ".")
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.WithZone("").String()
	}
	secure := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if cfg.CA != "" {
		if len(cfg.CA) > formats.DatabaseCertificateBytes {
			return nil, errors.New("CA 证书超过 64 KB")
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(cfg.CA)) {
			return nil, errors.New("无法读取 CA 证书，请选择 PEM 格式")
		}
		secure.RootCAs = pool
	}
	return secure, nil
}

func openDriver(cfg Config, proto Protocol, secure *tls.Config) (*sql.DB, error) {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dsn := url.URL{Host: addr, User: url.UserPassword(cfg.User, cfg.Password)}
	switch cfg.Engine {
	case "mysql":
		config := mysql.NewConfig()
		config.User, config.Passwd, config.Net, config.Addr, config.DBName = cfg.User, cfg.Password, "tcp", addr, cfg.Database
		config.Timeout, config.ReadTimeout, config.WriteTimeout, config.TLS = connectTimeout, 45*time.Second, connectTimeout, secure
		connector, err := mysql.NewConnector(config)
		if err != nil {
			return nil, err
		}
		return sql.OpenDB(connector), nil
	case "postgres":
		database := cfg.Database
		if database == "" {
			database = "postgres"
		}
		dsn.Scheme, dsn.Path, dsn.RawQuery = "postgres", "/"+database, "sslmode=disable"
		config, err := pgx.ParseConfig(dsn.String())
		if err != nil {
			return nil, err
		}
		config.Host, config.Port, config.User, config.Password = cfg.Host, uint16(cfg.Port), cfg.User, cfg.Password
		config.Database = database
		config.TLSConfig, config.Fallbacks, config.ConnectTimeout = secure, nil, connectTimeout
		config.RuntimeParams = map[string]string{"application_name": "ExcelLens", "default_transaction_read_only": "on"}
		return stdlib.OpenDB(*config), nil
	case "oracle":
		dsn.Scheme = "oracle"
		options := url.Values{"CONNECTION TIMEOUT": {"12"}, "PREFETCH_ROWS": {"8"}, "SSL VERIFY": {"true"}}
		if proto.ID == "sid" {
			options.Set("SID", cfg.Database)
		} else {
			dsn.Path = "/" + cfg.Database
		}
		if secure != nil {
			options.Set("SSL", "true")
		}
		dsn.RawQuery = options.Encode()
		connector := goora.NewConnector(dsn.String()).(*goora.OracleConnector)
		connector.WithTLSConfig(secure)
		return sql.OpenDB(connector), nil
	case "sqlserver":
		dsn.Scheme = "sqlserver"
		query := url.Values{"app name": {"ExcelLens"}, "encrypt": {"disable"}}
		if cfg.Database != "" {
			query.Set("database", cfg.Database)
		}
		if secure != nil {
			query.Set("encrypt", "true")
		}
		dsn.RawQuery = query.Encode()
		config, err := msdsn.Parse(dsn.String())
		if err != nil {
			return nil, err
		}
		if secure != nil {
			config.TLSConfig = secure
		}
		return sql.OpenDB(mssql.NewConnectorConfig(config)), nil
	case "clickhouse":
		protocol := clickhouse.Native
		var headers map[string]string
		if proto.ID == "http" {
			protocol = clickhouse.HTTP
			if secure != nil && cfg.Password == "" {
				// The driver otherwise treats blank-password HTTPS as client-certificate authentication.
				headers = map[string]string{"X-ClickHouse-SSL-Certificate-Auth": "off"}
			}
		}
		return clickhouse.OpenDB(&clickhouse.Options{Addr: []string{addr}, Protocol: protocol, TLS: secure,
			Auth:        clickhouse.Auth{Database: cfg.Database, Username: cfg.User, Password: cfg.Password},
			DialTimeout: connectTimeout, ReadTimeout: 45 * time.Second, BlockBufferSize: 1, HttpHeaders: headers}), nil
	}
	return nil, errors.New("数据库类型无效")
}

// Driver messages can include a DSN, user name, or query data. Report only
// identified failures and keep credentials out of logs, status and history.
func databaseError(err error, fallback string) error {
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var network net.Error
	var my *mysql.MySQLError
	var pg *pgconn.PgError
	var ms mssql.Error
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &network) && network.Timeout():
		return errors.New("数据库访问超时，请核对网络和服务器状态")
	case errors.As(err, &unknown), errors.As(err, &hostname), errors.As(err, &invalid):
		return errors.New("TLS 证书验证失败，请核对服务器名称或提供受信任的 CA")
	case errors.Is(err, syscall.ECONNREFUSED):
		return errors.New("服务器拒绝连接，请核对地址、端口和服务状态")
	case errors.As(err, &my) && (my.Number == 1044 || my.Number == 1045),
		errors.As(err, &pg) && strings.HasPrefix(pg.Code, "28"),
		errors.As(err, &ms) && ms.Number == 18456:
		return errors.New("账户验证失败，请核对用户名、密码和数据库访问权限")
	}
	return errors.New(fallback)
}
