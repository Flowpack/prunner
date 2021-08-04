package helper

import "errors"

func ErrToStrPtr(err error) *string {
	if err != nil {
		s := err.Error()
		return &s
	}
	return nil
}

func StrPtrToErr(s *string) error {
	if s == nil || *s == "" {
		return nil
	}
	return errors.New(*s)
}
