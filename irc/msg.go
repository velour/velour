package irc

// Parsing of IRC messages as specified in RFC 1459.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// A Msg is the basic unit of communication
// in the IRC protocol.
type Msg struct {
	// Raw is the raw message string.
	Raw string

	// Origin is either the nick or server that
	// originated the message.
	Origin string

	// User is the user name of the user that
	// originated the message.
	//
	// This field is typically set in server to
	// client communication when the
	// message originated from a client.
	User string

	// Host is the host name of the user that
	// originated the message.
	//
	// This field is typically set in server to
	// client communication when the
	// message originated from a client.
	Host string

	// Cmd is the command.
	Cmd string

	// Args is the argument list.
	Args []string
}

// RawString returns the raw string representation
// of a message.  If Raw is non-empty then it is
// returned, otherwise a raw string is built from
// the fields of the message.  If there is an error
// generating the raw string then the string is
// invalid and an error is returned.
func (m Msg) RawString() (string, error) {
	raw := ""
	if m.Raw != "" {
		raw = m.Raw
		goto out
	}
	if m.Origin != "" {
		raw += ":" + m.Origin
		if m.User != "" {
			raw += "!" + m.User + "@" + m.Host
		}
		raw += " "
	}
	raw += m.Cmd
	for i, a := range m.Args {
		if i == len(m.Args)-1 {
			raw += " :" + a
		} else {
			raw += " " + a
		}
	}
out:
	if len(raw) > MaxMsgLength-len(MsgMarker) {
		return "", MsgTooLong{raw, len(raw) - (MaxMsgLength - len(MsgMarker))}
	}
	return strings.TrimRight(raw, "\n"), nil
}

// ParseMsg parses a message from
// a raw message string.
// BUG(eaburns): doesn't validate the message.
func ParseMsg(data string) (Msg, error) {
	var msg Msg
	msg.Raw = data

	if data[0] == ':' {
		var prefix string
		prefix, data = splitString(data[1:], ' ')
		msg.Origin, prefix = splitString(prefix, '!')
		msg.User, msg.Host = splitString(prefix, '@')
	}

	msg.Cmd, data = splitString(data, ' ')

	for len(data) > 0 {
		var arg string
		if data[0] == ':' {
			arg, data = data[1:], ""
		} else {
			arg, data = splitString(data, ' ')
		}
		msg.Args = append(msg.Args, arg)
	}
	return msg, nil
}

// readMsg returns the next message from
// the stream.  If error is non-nil then the message
// is not valid.
func readMsg(in *bufio.Reader) (Msg, error) {
	data, err := readMsgData(in)
	if err != nil {
		if long, ok := err.(MsgTooLong); ok {
			m, err := ParseMsg(long.Msg)
			if err != nil {
				return Msg{}, err
			}
			return m, long
		}
		return Msg{}, err
	}
	return ParseMsg(data)
}

// splitStrings returns two strings, the first
// is the portion of the string before the
// delimiter and the second is the portion
// after the delimiter.  If the delimiter is not
// in the string then the entire string is
// before the delimiter.
//
// If the delimiter is a space ' ' then the
// second argument has all leading
// space characters stripped.
func splitString(s string, delim rune) (string, string) {
	i := strings.IndexRune(s, delim)
	if i < 0 {
		return s, ""
	}
	if delim != ' ' {
		return s[:i], s[i+1:]
	}
	fst := s[:i]
	for ; i < len(s) && s[i] == ' '; i++ {
	}
	return fst, s[i:]
}

// MaxMsgLength is the maximum length
// of a message in bytes.
const MaxMsgLength = 512

// MsgMarker is the marker delineating messages
// in the TCP stream.
const MsgMarker = "\r\n"

// MsgTooLong is returned as an error for a message that is longer than MaxMsgLength bytes.
type MsgTooLong struct {
	// Msg is the truncated message text.
	Msg string
	// NTrunc is the number of truncated bytes.
	NTrunc int
}

func (m MsgTooLong) Error() string {
	return fmt.Sprintf("Message is too long (%d bytes truncated): %s", m.NTrunc, m.Msg)
}

// readMsgData returns the raw data for the
// next message from the stream.  On error the
// returned string will be empty.
func readMsgData(in *bufio.Reader) (string, error) {
	var msg []byte
	for {
		switch c, err := in.ReadByte(); {
		case err == io.EOF && len(msg) > 0:
			return "", unexpected("end of file")

		case err != nil:
			return "", err

		case c == '\000':
			return "", unexpected("null")

		case c == '\n':
			// Technically an invalid message, but instead we just strip it.

		case c == '\r':
			c, err = in.ReadByte()
			if err != nil {
				if err == io.EOF {
					err = unexpected("end of file")
				}
				return "", err
			}
			if c != '\n' {
				return "", unexpected("carrage return")
			}
			if len(msg) == 0 {
				continue
			}
			return string(msg), nil

		case len(msg) >= MaxMsgLength-2:
			n, _ := junk(in)
			return "", MsgTooLong{Msg: string(msg[:len(msg)-1]), NTrunc: n + 1}

		default:
			msg = append(msg, c)
		}
	}
}

// Junk reads and discards bytes until the next
// message marker is found, returning the number
// of discarded non-marker bytes.
func junk(in *bufio.Reader) (int, error) {
	var last byte
	n := 0
	for {
		c, err := in.ReadByte()
		if err != nil {
			return n, err
		}
		n++
		if last == MsgMarker[0] && c == MsgMarker[1] {
			break
		}
		last = c
	}
	return n - 1, nil
}

// unexpected returns an error that describes
// the recite of an unexpected character
// in the message stream.
func unexpected(what string) error {
	return errors.New("unexpected " + what + " in message stream")
}

// Cmd names as listed in RFC 2812.
const (
	PASS                  = "PASS"
	NICK                  = "NICK"
	USER                  = "USER"
	OPER                  = "OPER"
	MODE                  = "MODE"
	SERVICE               = "SERVICE"
	QUIT                  = "QUIT"
	SQUIT                 = "SQUIT"
	JOIN                  = "JOIN"
	PART                  = "PART"
	TOPIC                 = "TOPIC"
	NAMES                 = "NAMES"
	LIST                  = "LIST"
	INVITE                = "INVITE"
	KICK                  = "KICK"
	PRIVMSG               = "PRIVMSG"
	NOTICE                = "NOTICE"
	MOTD                  = "MOTD"
	LUSERS                = "LUSERS"
	VERSION               = "VERSION"
	STATS                 = "STATS"
	LINKS                 = "LINKS"
	TIME                  = "TIME"
	CONNECT               = "CONNECT"
	TRACE                 = "TRACE"
	ADMIN                 = "ADMIN"
	INFO                  = "INFO"
	SERVLIST              = "SERVLIST"
	SQUERY                = "SQUERY"
	WHO                   = "WHO"
	WHOIS                 = "WHOIS"
	WHOWAS                = "WHOWAS"
	KILL                  = "KILL"
	PING                  = "PING"
	PONG                  = "PONG"
	ERROR                 = "ERROR"
	AWAY                  = "AWAY"
	REHASH                = "REHASH"
	DIE                   = "DIE"
	RESTART               = "RESTART"
	SUMMON                = "SUMMON"
	USERS                 = "USERS"
	WALLOPS               = "WALLOPS"
	USERHOST              = "USERHOST"
	ISON                  = "ISON"
	RPL_WELCOME           = "001"
	RPL_YOURHOST          = "002"
	RPL_CREATED           = "003"
	RPL_MYINFO            = "004"
	RPL_BOUNCE            = "005"
	RPL_USERHOST          = "302"
	RPL_ISON              = "303"
	RPL_AWAY              = "301"
	RPL_UNAWAY            = "305"
	RPL_NOWAWAY           = "306"
	RPL_WHOISUSER         = "311"
	RPL_WHOISSERVER       = "312"
	RPL_WHOISOPERATOR     = "313"
	RPL_WHOISIDLE         = "317"
	RPL_ENDOFWHOIS        = "318"
	RPL_WHOISCHANNELS     = "319"
	RPL_WHOWASUSER        = "314"
	RPL_ENDOFWHOWAS       = "369"
	RPL_LISTSTART         = "321"
	RPL_LIST              = "322"
	RPL_LISTEND           = "323"
	RPL_UNIQOPIS          = "325"
	RPL_CHANNELMODEIS     = "324"
	RPL_NOTOPIC           = "331"
	RPL_TOPIC             = "332"
	RPL_TOPICWHOTIME      = "333" // ircu specific (not in the RFC)
	RPL_INVITING          = "341"
	RPL_SUMMONING         = "342"
	RPL_INVITELIST        = "346"
	RPL_ENDOFINVITELIST   = "347"
	RPL_EXCEPTLIST        = "348"
	RPL_ENDOFEXCEPTLIST   = "349"
	RPL_VERSION           = "351"
	RPL_WHOREPLY          = "352"
	RPL_ENDOFWHO          = "315"
	RPL_NAMREPLY          = "353"
	RPL_ENDOFNAMES        = "366"
	RPL_LINKS             = "364"
	RPL_ENDOFLINKS        = "365"
	RPL_BANLIST           = "367"
	RPL_ENDOFBANLIST      = "368"
	RPL_INFO              = "371"
	RPL_ENDOFINFO         = "374"
	RPL_MOTDSTART         = "375"
	RPL_MOTD              = "372"
	RPL_ENDOFMOTD         = "376"
	RPL_YOUREOPER         = "381"
	RPL_REHASHING         = "382"
	RPL_YOURESERVICE      = "383"
	RPL_TIME              = "391"
	RPL_USERSSTART        = "392"
	RPL_USERS             = "393"
	RPL_ENDOFUSERS        = "394"
	RPL_NOUSERS           = "395"
	RPL_TRACELINK         = "200"
	RPL_TRACECONNECTING   = "201"
	RPL_TRACEHANDSHAKE    = "202"
	RPL_TRACEUNKNOWN      = "203"
	RPL_TRACEOPERATOR     = "204"
	RPL_TRACEUSER         = "205"
	RPL_TRACESERVER       = "206"
	RPL_TRACESERVICE      = "207"
	RPL_TRACENEWTYPE      = "208"
	RPL_TRACECLASS        = "209"
	RPL_TRACERECONNECT    = "210"
	RPL_TRACELOG          = "261"
	RPL_TRACEEND          = "262"
	RPL_STATSLINKINFO     = "211"
	RPL_STATSCOMMANDS     = "212"
	RPL_ENDOFSTATS        = "219"
	RPL_STATSUPTIME       = "242"
	RPL_STATSOLINE        = "243"
	RPL_UMODEIS           = "221"
	RPL_SERVLIST          = "234"
	RPL_SERVLISTEND       = "235"
	RPL_LUSERCLIENT       = "251"
	RPL_LUSEROP           = "252"
	RPL_LUSERUNKNOWN      = "253"
	RPL_LUSERCHANNELS     = "254"
	RPL_LUSERME           = "255"
	RPL_ADMINME           = "256"
	RPL_ADMINLOC1         = "257"
	RPL_ADMINLOC2         = "258"
	RPL_ADMINEMAIL        = "259"
	RPL_TRYAGAIN          = "263"
	ERR_NOSUCHNICK        = "401"
	ERR_NOSUCHSERVER      = "402"
	ERR_NOSUCHCHANNEL     = "403"
	ERR_CANNOTSENDTOCHAN  = "404"
	ERR_TOOMANYCHANNELS   = "405"
	ERR_WASNOSUCHNICK     = "406"
	ERR_TOOMANYTARGETS    = "407"
	ERR_NOSUCHSERVICE     = "408"
	ERR_NOORIGIN          = "409"
	ERR_NORECIPIENT       = "411"
	ERR_NOTEXTTOSEND      = "412"
	ERR_NOTOPLEVEL        = "413"
	ERR_WILDTOPLEVEL      = "414"
	ERR_BADMASK           = "415"
	ERR_UNKNOWNCOMMAND    = "421"
	ERR_NOMOTD            = "422"
	ERR_NOADMININFO       = "423"
	ERR_FILEERROR         = "424"
	ERR_NONICKNAMEGIVEN   = "431"
	ERR_ERRONEUSNICKNAME  = "432"
	ERR_NICKNAMEINUSE     = "433"
	ERR_NICKCOLLISION     = "436"
	ERR_UNAVAILRESOURCE   = "437"
	ERR_USERNOTINCHANNEL  = "441"
	ERR_NOTONCHANNEL      = "442"
	ERR_USERONCHANNEL     = "443"
	ERR_NOLOGIN           = "444"
	ERR_SUMMONDISABLED    = "445"
	ERR_USERSDISABLED     = "446"
	ERR_NOTREGISTERED     = "451"
	ERR_NEEDMOREPARAMS    = "461"
	ERR_ALREADYREGISTRED  = "462"
	ERR_NOPERMFORHOST     = "463"
	ERR_PASSWDMISMATCH    = "464"
	ERR_YOUREBANNEDCREEP  = "465"
	ERR_YOUWILLBEBANNED   = "466"
	ERR_KEYSET            = "467"
	ERR_CHANNELISFULL     = "471"
	ERR_UNKNOWNMODE       = "472"
	ERR_INVITEONLYCHAN    = "473"
	ERR_BANNEDFROMCHAN    = "474"
	ERR_BADCHANNELKEY     = "475"
	ERR_BADCHANMASK       = "476"
	ERR_NOCHANMODES       = "477"
	ERR_BANLISTFULL       = "478"
	ERR_NOPRIVILEGES      = "481"
	ERR_CHANOPRIVSNEEDED  = "482"
	ERR_CANTKILLSERVER    = "483"
	ERR_RESTRICTED        = "484"
	ERR_UNIQOPPRIVSNEEDED = "485"
	ERR_NOOPERHOST        = "491"
	ERR_UMODEUNKNOWNFLAG  = "501"
	ERR_USERSDONTMATCH    = "502"
)

// CmdNames is a map from command strings to their names.
var CmdNames = map[string]string{
	PASS:     "PASS",
	NICK:     "NICK",
	USER:     "USER",
	OPER:     "OPER",
	MODE:     "MODE",
	SERVICE:  "SERVICE",
	QUIT:     "QUIT",
	SQUIT:    "SQUIT",
	JOIN:     "JOIN",
	PART:     "PART",
	TOPIC:    "TOPIC",
	NAMES:    "NAMES",
	LIST:     "LIST",
	INVITE:   "INVITE",
	KICK:     "KICK",
	PRIVMSG:  "PRIVMSG",
	NOTICE:   "NOTICE",
	MOTD:     "MOTD",
	LUSERS:   "LUSERS",
	VERSION:  "VERSION",
	STATS:    "STATS",
	LINKS:    "LINKS",
	TIME:     "TIME",
	CONNECT:  "CONNECT",
	TRACE:    "TRACE",
	ADMIN:    "ADMIN",
	INFO:     "INFO",
	SERVLIST: "SERVLIST",
	SQUERY:   "SQUERY",
	WHO:      "WHO",
	WHOIS:    "WHOIS",
	WHOWAS:   "WHOWAS",
	KILL:     "KILL",
	PING:     "PING",
	PONG:     "PONG",
	ERROR:    "ERROR",
	AWAY:     "AWAY",
	REHASH:   "REHASH",
	DIE:      "DIE",
	RESTART:  "RESTART",
	SUMMON:   "SUMMON",
	USERS:    "USERS",
	WALLOPS:  "WALLOPS",
	USERHOST: "USERHOST",
	ISON:     "ISON",
	"001":    "RPL_WELCOME",
	"002":    "RPL_YOURHOST",
	"003":    "RPL_CREATED",
	"004":    "RPL_MYINFO",
	"005":    "RPL_BOUNCE",
	"302":    "RPL_USERHOST",
	"303":    "RPL_ISON",
	"301":    "RPL_AWAY",
	"305":    "RPL_UNAWAY",
	"306":    "RPL_NOWAWAY",
	"311":    "RPL_WHOISUSER",
	"312":    "RPL_WHOISSERVER",
	"313":    "RPL_WHOISOPERATOR",
	"317":    "RPL_WHOISIDLE",
	"318":    "RPL_ENDOFWHOIS",
	"319":    "RPL_WHOISCHANNELS",
	"314":    "RPL_WHOWASUSER",
	"369":    "RPL_ENDOFWHOWAS",
	"321":    "RPL_LISTSTART",
	"322":    "RPL_LIST",
	"323":    "RPL_LISTEND",
	"325":    "RPL_UNIQOPIS",
	"324":    "RPL_CHANNELMODEIS",
	"331":    "RPL_NOTOPIC",
	"332":    "RPL_TOPIC",
	"333":    "RPL_TOPICWHOTIME", // ircu specific (not in the RFC)
	"341":    "RPL_INVITING",
	"342":    "RPL_SUMMONING",
	"346":    "RPL_INVITELIST",
	"347":    "RPL_ENDOFINVITELIST",
	"348":    "RPL_EXCEPTLIST",
	"349":    "RPL_ENDOFEXCEPTLIST",
	"351":    "RPL_VERSION",
	"352":    "RPL_WHOREPLY",
	"315":    "RPL_ENDOFWHO",
	"353":    "RPL_NAMREPLY",
	"366":    "RPL_ENDOFNAMES",
	"364":    "RPL_LINKS",
	"365":    "RPL_ENDOFLINKS",
	"367":    "RPL_BANLIST",
	"368":    "RPL_ENDOFBANLIST",
	"371":    "RPL_INFO",
	"374":    "RPL_ENDOFINFO",
	"375":    "RPL_MOTDSTART",
	"372":    "RPL_MOTD",
	"376":    "RPL_ENDOFMOTD",
	"381":    "RPL_YOUREOPER",
	"382":    "RPL_REHASHING",
	"383":    "RPL_YOURESERVICE",
	"391":    "RPL_TIME",
	"392":    "RPL_USERSSTART",
	"393":    "RPL_USERS",
	"394":    "RPL_ENDOFUSERS",
	"395":    "RPL_NOUSERS",
	"200":    "RPL_TRACELINK",
	"201":    "RPL_TRACECONNECTING",
	"202":    "RPL_TRACEHANDSHAKE",
	"203":    "RPL_TRACEUNKNOWN",
	"204":    "RPL_TRACEOPERATOR",
	"205":    "RPL_TRACEUSER",
	"206":    "RPL_TRACESERVER",
	"207":    "RPL_TRACESERVICE",
	"208":    "RPL_TRACENEWTYPE",
	"209":    "RPL_TRACECLASS",
	"210":    "RPL_TRACERECONNECT",
	"261":    "RPL_TRACELOG",
	"262":    "RPL_TRACEEND",
	"211":    "RPL_STATSLINKINFO",
	"212":    "RPL_STATSCOMMANDS",
	"219":    "RPL_ENDOFSTATS",
	"242":    "RPL_STATSUPTIME",
	"243":    "RPL_STATSOLINE",
	"221":    "RPL_UMODEIS",
	"234":    "RPL_SERVLIST",
	"235":    "RPL_SERVLISTEND",
	"251":    "RPL_LUSERCLIENT",
	"252":    "RPL_LUSEROP",
	"253":    "RPL_LUSERUNKNOWN",
	"254":    "RPL_LUSERCHANNELS",
	"255":    "RPL_LUSERME",
	"256":    "RPL_ADMINME",
	"257":    "RPL_ADMINLOC",
	"258":    "RPL_ADMINLOC",
	"259":    "RPL_ADMINEMAIL",
	"263":    "RPL_TRYAGAIN",
	"401":    "ERR_NOSUCHNICK",
	"402":    "ERR_NOSUCHSERVER",
	"403":    "ERR_NOSUCHCHANNEL",
	"404":    "ERR_CANNOTSENDTOCHAN",
	"405":    "ERR_TOOMANYCHANNELS",
	"406":    "ERR_WASNOSUCHNICK",
	"407":    "ERR_TOOMANYTARGETS",
	"408":    "ERR_NOSUCHSERVICE",
	"409":    "ERR_NOORIGIN",
	"411":    "ERR_NORECIPIENT",
	"412":    "ERR_NOTEXTTOSEND",
	"413":    "ERR_NOTOPLEVEL",
	"414":    "ERR_WILDTOPLEVEL",
	"415":    "ERR_BADMASK",
	"421":    "ERR_UNKNOWNCOMMAND",
	"422":    "ERR_NOMOTD",
	"423":    "ERR_NOADMININFO",
	"424":    "ERR_FILEERROR",
	"431":    "ERR_NONICKNAMEGIVEN",
	"432":    "ERR_ERRONEUSNICKNAME",
	"433":    "ERR_NICKNAMEINUSE",
	"436":    "ERR_NICKCOLLISION",
	"437":    "ERR_UNAVAILRESOURCE",
	"441":    "ERR_USERNOTINCHANNEL",
	"442":    "ERR_NOTONCHANNEL",
	"443":    "ERR_USERONCHANNEL",
	"444":    "ERR_NOLOGIN",
	"445":    "ERR_SUMMONDISABLED",
	"446":    "ERR_USERSDISABLED",
	"451":    "ERR_NOTREGISTERED",
	"461":    "ERR_NEEDMOREPARAMS",
	"462":    "ERR_ALREADYREGISTRED",
	"463":    "ERR_NOPERMFORHOST",
	"464":    "ERR_PASSWDMISMATCH",
	"465":    "ERR_YOUREBANNEDCREEP",
	"466":    "ERR_YOUWILLBEBANNED",
	"467":    "ERR_KEYSET",
	"471":    "ERR_CHANNELISFULL",
	"472":    "ERR_UNKNOWNMODE",
	"473":    "ERR_INVITEONLYCHAN",
	"474":    "ERR_BANNEDFROMCHAN",
	"475":    "ERR_BADCHANNELKEY",
	"476":    "ERR_BADCHANMASK",
	"477":    "ERR_NOCHANMODES",
	"478":    "ERR_BANLISTFULL",
	"481":    "ERR_NOPRIVILEGES",
	"482":    "ERR_CHANOPRIVSNEEDED",
	"483":    "ERR_CANTKILLSERVER",
	"484":    "ERR_RESTRICTED",
	"485":    "ERR_UNIQOPPRIVSNEEDED",
	"491":    "ERR_NOOPERHOST",
	"501":    "ERR_UMODEUNKNOWNFLAG",
	"502":    "ERR_USERSDONTMATCH",
}
