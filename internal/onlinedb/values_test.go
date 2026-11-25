package onlinedb

import (
	"testing"
	"time"
)

func TestDriverTypesPreserveOriginalValues(t *testing.T) {
	local := time.Date(2026, 11, 25, 10, 20, 30, 321000000, time.FixedZone("UTC+8", 8*3600))
	cases := []struct {
		engine, name string
		driverValue  any
		want         Value
	}{
		{"sqlserver", "UNIQUEIDENTIFIER", []byte{0x67, 0x45, 0x23, 0x01, 0xAB, 0x89, 0xEF, 0xCD, 0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF}, Value{Text, "01234567-89AB-CDEF-0123-456789ABCDEF"}},
		{"sqlserver", "UNIQUEIDENTIFIER", nil, Value{Kind: Null}},
		{"sqlserver", "BIT", true, Value{Boolean, "1"}},
		{"oracle", "OCIBlobLocator", []byte{0x00, 0xFE, 0xFF}, Value{Binary, "0x00feff"}},
		{"oracle", "OCIClobLocator", "合计", Value{Text, "合计"}},
		{"oracle", "TimeStampeLTZ", local, Value{Date, "2026-11-25T10:20:30.321+08:00"}},
		{"oracle", "DATE", local, Value{Date, "2026-11-25T10:20:30.321"}},
		{"mysql", "UNSIGNED BIGINT", []byte("18446744073709551615"), Value{Number, "18446744073709551615"}},
	}
	for _, tc := range cases {
		t.Run(tc.engine+"/"+tc.name, func(t *testing.T) {
			got, err := readValue(databaseColumnKind(tc.engine, tc.name), tc.driverValue)
			if err != nil || got != tc.want {
				t.Fatalf("got %#v (%v), want %#v", got, err, tc.want)
			}
		})
	}
	if _, err := readValue(databaseColumnKind("sqlserver", "UNIQUEIDENTIFIER"), []byte("malformed")); err == nil {
		t.Fatal("accepted an invalid SQL Server identifier")
	}
}
