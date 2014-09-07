package irc

import (
	"bufio"
	"io"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestReadMsgOK(t *testing.T) {
	tests := []Msg{
		{
			Raw:    ":e!foo@bar.com JOIN #test54321",
			Origin: "e",
			User:   "foo",
			Host:   "bar.com",
			Cmd:    "JOIN",
			Args:   []string{"#test54321"},
		},
		{
			Raw:    ":e JOIN #test54321",
			Origin: "e",
			Cmd:    "JOIN",
			Args:   []string{"#test54321"},
		},
		{
			Raw:  "JOIN #test54321",
			Cmd:  "JOIN",
			Args: []string{"#test54321"},
		},
		{
			Raw:  "JOIN #test54321 :foo bar",
			Cmd:  "JOIN",
			Args: []string{"#test54321", "foo bar"},
		},
		{
			Raw:  "JOIN #test54321 ::foo bar",
			Cmd:  "JOIN",
			Args: []string{"#test54321", ":foo bar"},
		},
		{
			Raw:  "JOIN    #test54321    foo       bar   ",
			Cmd:  "JOIN",
			Args: []string{"#test54321", "foo", "bar"},
		},
		{
			Raw:  "JOIN :",
			Cmd:  "JOIN",
			Args: []string{""},
		},
	}

	for _, test := range tests {
		m, err := ParseMsg(test.Raw)
		if err != nil {
			t.Errorf(err.Error())
		}
		if !reflect.DeepEqual(m, test) {
			t.Errorf("failed to correctly parse %#v\nGot: %#v", test, m)
		}
	}
}

func TestReadMsgDataOk(t *testing.T) {
	max := make([]byte, MaxMsgLength)
	for i := range max {
		max[i] = 'a'
	}
	max[len(max)-2] = '\r'
	max[len(max)-1] = '\n'

	tests := []struct {
		s  string
		ms []string
	}{
		{"a\r\nb\r\nc\r\n", []string{"a", "b", "c"}},
		{"a\r\nb\r\n\r\nc\r\n", []string{"a", "b", "c"}},
		{"a \r\nb	\r\n\r\nc\r\n", []string{"a ", "b	", "c"}},
		{
			":e!foo@bar.com JOIN #test54321\r\n",
			[]string{":e!foo@bar.com JOIN #test54321"},
		},
		{string(max), []string{string(max[:len(max)-2])}},
	}

	for _, test := range tests {
		in := bufio.NewReader(strings.NewReader(test.s))
		i := 0
		for {
			m, err := readMsgData(in)
			if err == io.EOF && i == len(test.ms) {
				break
			}
			if i >= len(test.ms) {
				t.Errorf("expected end of messages")
			}
			if err != nil {
				t.Errorf(err.Error())
			}
			if m != test.ms[i] {
				t.Errorf("expected message %s, got %s",
					test.ms[i], m)
			}
			i++
		}
	}
}

func TestReadMsgDataError(t *testing.T) {
	tooLong := make([]byte, MaxMsgLength)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	tooLong[len(tooLong)-1] = '\r'

	tests := []struct {
		s      string
		errStr string
	}{
		{"a", "unexpected end of file in message stream"},
		{"a\r\r\n", "unexpected carrage return in message stream"},
		{"hello there\000\r\n", "unexpected null in message stream"},
		{string(tooLong), "Message is too long.*"},
	}

	for _, test := range tests {
		in := bufio.NewReader(strings.NewReader(test.s))
		_, err := readMsgData(in)
		if err == nil {
			t.Errorf("expected error [%s], got none", test.errStr)
		} else if matched, _ := regexp.MatchString(test.errStr, err.Error()); !matched {
			t.Errorf("unexpected error [%s], expected [%s]",
				err, test.errStr)
		}
	}
}
