package i18n

import (
	"errors"
	"strings"
)

type messageError struct {
	key   Key
	args  Args
	cause error
}

func Error(key Key, args ...Args) error {
	return Wrap(key, nil, args...)
}

func Wrap(key Key, cause error, args ...Args) error {
	var values Args
	if len(args) > 0 {
		values = args[0]
	}
	return &messageError{key: key, args: values, cause: cause}
}

func (e *messageError) Unwrap() error { return e.cause }
func (e *messageError) Error() string { return e.text(Default) }

func (e *messageError) text(locale Locale) string {
	values := make(Args, len(e.args))
	for name, value := range e.args {
		if nested, ok := value.(error); ok {
			value = locale.ErrorText(nested)
		}
		values[name] = value
	}
	if e.cause != nil {
		values["detail"] = locale.ErrorText(e.cause)
	}
	return locale.Text(e.key, values)
}

func ErrorKey(err error) Key {
	var message *messageError
	if errors.As(err, &message) {
		return message.key
	}
	return ""
}

// ErrorText keeps the underlying diagnostic when no source message was assigned,
// including operating system and database driver errors.
func (locale Locale) ErrorText(err error) string {
	if err == nil {
		return ""
	}
	if message, ok := err.(*messageError); ok {
		return message.text(locale)
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		values := []string{}
		for _, cause := range joined.Unwrap() {
			values = append(values, locale.ErrorText(cause))
		}
		return strings.Join(values, "\n")
	}
	return err.Error()
}
