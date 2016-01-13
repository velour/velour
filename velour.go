// Velour is an IRC client for the acme editor.

package main

import (
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	osuser "os/user"
	"sort"
	"strings"
	"time"

	"github.com/velour/velour/irc"
)

const (
	// DefaultPort is the port used to connect to
	// the server if one is not specified.
	defaultPort = "6667"

	// InitialTimeout is the initial amount of time
	// to delay before reconnecting.  Each failed
	// reconnection doubles the timout until
	// a connection is made successfully.
	initialTimeout = 2 * time.Second

	// PingTime is the amount of inactive time
	// to wait before sending a ping to the server.
	pingTime = 120 * time.Second

	// NickServer is the nick name of the nick server.
	nickServer = "NickServ"
)

var (
	nick     = flag.String("n", username(), "nickname")
	full     = flag.String("f", name(), "full name")
	pass     = flag.String("p", "", "password")
	debug    = flag.Bool("d", false, "debugging")
	util     = flag.String("u", "", "utility program")
	join     = flag.String("j", "", "automatically join a channel")
	ssl      = flag.Bool("ssl", false, "use SSL to connect to the server")
	trustSsl = flag.Bool("trust", false, "don't verify server's SSL certificate")
)

var (
	// client is the IRC client connection.
	client *irc.Client

	// Server is the server's address.
	server = ""

	// serverWin is the server win.
	serverWin *win

	// winEvents multiplexes all win events.
	winEvents = make(chan winEvent)

	// Quitting is set to true if the user Dels
	// the server window.
	quitting = false
)

var wins = map[string]*win{}

func getWin(target string) *win {
	key := strings.ToLower(target)
	w, ok := wins[key]
	if !ok {
		w = newWin(target)
		wins[key] = w
	}
	return w
}

func main() {
	flag.Usage = func() {
		os.Stdout.WriteString("usage: velour [options] <server>[:<port>]\n")
		flag.PrintDefaults()
		os.Stdout.WriteString("The utility program given by the -u flag will receive a nick as the first argument, and the content of a message on standard input.\n")
	}
	flag.Parse()
	if len(flag.Args()) != 1 {
		flag.Usage()
		os.Exit(1)
	}

	var err error
	var port string
	if server, port, err = net.SplitHostPort(flag.Arg(0)); err != nil {
		port = defaultPort
		server = flag.Arg(0)
	}

	serverWin = newWin("")
	if !*debug {
		defer func() {
			serverWin.del()
			for _, win := range wins {
				win.del()
			}
		}()
	}
	serverWin.Fprintf("tag", "Chat ")
	// Set Dump handling for the server window.
	if wd, err := os.Getwd(); err != nil {
		log.Println("Failed to set dump working directory: " + err.Error())
	} else {
		args := make([]string, 0, len(os.Args))
		for _, arg := range os.Args {
			args = append(args, quote(arg))
		}
		serverWin.Ctl("dumpdir %s", wd)
		serverWin.Ctl("dump %s", strings.Join(args, " "))
	}

	errors := 0
	for {
		handleConnecting(connect(server + ":" + port))

		serverWin.WriteString("Connected")
		for _, w := range wins {
			w.WriteString("Connected")
			if len(w.target) > 0 && w.target[0] == '#' {
				client.Out <- irc.Msg{Cmd: irc.JOIN, Args: []string{w.target}}
			}
		}

		begin := time.Now()
		handleConnection()

		d := time.Now().Sub(begin)
		if d < 1*time.Minute {
			errors++
		} else {
			errors = 0
		}

		if quitting || errors > 4 {
			break
		}
	}
}

// Connect returns a channel upon which the
// value true is sent when a connection with
// the server is successfully established.
func connect(addr string) <-chan bool {
	conn := make(chan bool)
	go func(chan<- bool) {
		timeout := initialTimeout
		for {
			var err error
			if *ssl {
				client, err = irc.DialSSL(addr, *nick, *full, *pass, *trustSsl)
			} else {
				client, err = irc.Dial(addr, *nick, *full, *pass)
			}
			if err == nil {
				conn <- true
				return
			}
			serverWin.WriteString("Failed to connect: " + err.Error())
			timeout *= 2
			<-time.After(timeout)
		}
	}(conn)
	return conn
}

// HandleConnecting handles window events while
// attempting to connect to the server.
func handleConnecting(conn <-chan bool) {
	for {
		select {
		case <-conn:
			return

		case ev := <-winEvents:
			if ev.timeStamp {
				continue
			}
			switch {
			case ev.C2 == 'x' || ev.C2 == 'X':
				fs := strings.Fields(string(ev.Text))
				if len(fs) > 0 && fs[0] == "Del" {
					if ev.win == serverWin {
						exit(0, "Quit")
					}
					ev.win.del()
				}

			case (ev.C1 == 'M' || ev.C1 == 'K') && ev.C2 == 'I':
				// Disallow typing while not connected
				ev.win.Addr("#%d,#%d", ev.Q0, ev.Q1)
				ev.win.writeData([]byte{0})
				ev.win.Addr("#%d", ev.win.pAddr)

			case (ev.C1 == 'M' || ev.C1 == 'K') && ev.C2 == 'D':
				ev.deleting(ev.Q0, ev.Q1)

			case ev.C2 == 'l' || ev.C2 == 'L':
				if ev.Flag&2 != 0 { // expansion
					// The look was on highlighted text.  Instead of
					// sending the hilighted text, send the original
					// addresses, so that 3-clicking without draging
					// on selected text doesn't move the cursor
					// out of the tag.
					ev.Q0 = ev.OrigQ0
					ev.Q1 = ev.OrigQ1
				}
				ev.WriteEvent(ev.Event)
			}
		}
	}
}

// HandleConnection handles events while
// connected to a server.
func handleConnection() {
	t := time.NewTimer(pingTime)

	defer func() {
		t.Stop()
		close(client.Out)
		serverWin.WriteString("Disconnected")
		serverWin.Ctl("clean")
		for _, w := range wins {
			w.WriteString("Disconnected")
			w.users = make(map[string]*user)
			w.lastSpeaker = ""
			w.Ctl("clean")
		}
		for err := range client.Errors {
			if err != io.EOF {
				log.Println(err)
			}
		}
	}()

	if *join != "" {
		client.Out <- irc.Msg{Cmd: irc.JOIN, Args: []string{*join}}
		*join = ""
	}

	for {
		select {
		case ev := <-winEvents:
			if ev.timeStamp {
				ev.win.printTimeStamp()
			} else {
				handleWindowEvent(ev)
			}

		case msg, ok := <-client.In:
			if !ok { // disconnect
				return
			}
			t.Reset(pingTime)
			handleMsg(msg)

		case <-t.C:
			client.Out <- irc.Msg{Cmd: irc.PING, Args: []string{client.Server}}
			t = time.NewTimer(pingTime)

		case err, ok := <-client.Errors:
			if ok {
				long, il := err.(irc.MsgTooLong)
				if !il && err != io.EOF {
					log.Println(err)
					return
				}
				if il {
					log.Println("Truncated", long.NTrunc, "bytes from message")
				}
			}
		}
	}
}

// HandleWindowEvent handles events from
// any of the acme wins.
func handleWindowEvent(ev winEvent) {
	if *debug {
		log.Printf("%#v\nText=[%s]\n\n", *ev.Event, string(ev.Text))
	}

	switch {
	case ev.C2 == 'x' || ev.C2 == 'X':
		text := strings.TrimSpace(string(ev.Text))
		if name, ok := extractName(ev.win, text); ok {
			ev.writeToPrompt(name + ", ")
			return
		}
		fs := strings.Fields(text)
		if len(fs) > 0 && handleExecute(ev, fs[0], fs[1:]) {
			return
		}
		if ev.Flag&1 != 0 { // acme recognized built-in command
			if ev.Flag&2 != 0 {
				ev.Q0 = ev.OrigQ0
				ev.Q1 = ev.OrigQ1
			}
			ev.WriteEvent(ev.Event)
			return
		}
		ev.writeToPrompt(text)

	case (ev.C1 == 'M' || ev.C1 == 'K') && ev.C2 == 'I':
		ev.typing(ev.Q0, ev.Q1)

	case (ev.C1 == 'M' || ev.C1 == 'K') && ev.C2 == 'D':
		ev.deleting(ev.Q0, ev.Q1)

	case ev.C2 == 'l' || ev.C2 == 'L':
		if ev.Flag&2 != 0 { // expansion
			// The look was on highlighted text.  Instead of
			// sending the hilighted text, send the original
			// addresses, so that 3-clicking without dragging
			// on selected text doesn't move the cursor
			// out of the tag.
			ev.Q0 = ev.OrigQ0
			ev.Q1 = ev.OrigQ1
		}
		ev.WriteEvent(ev.Event)
	}
}

// extractName returns the name and true if the text is a user's name,
// in either "raw" or <name> format.
func extractName(w *win, text string) (string, bool) {
	if len(text) == 0 {
		return "", false
	}
	name := text
	if text[0] == '<' && text[len(text)-1] == '>' {
		name = text[1 : len(text)-1]
	}
	return name, w.users[name] != nil
}

// HandleExecute handles acme execte commands.
func handleExecute(ev winEvent, cmd string, args []string) bool {
	switch cmd {
	case "Debug":
		*debug = !*debug

	case "Del":
		t := ev.target
		if ev.win == serverWin {
			quitting = true
			client.Out <- irc.Msg{Cmd: irc.QUIT}
		} else if t != "" && t[0] == '#' { // channel
			client.Out <- irc.Msg{Cmd: irc.PART, Args: []string{t}}
		} else { // private chat
			ev.win.del()
		}

	case "Chat":
		if len(args) != 1 {
			break
		}
		if args[0][0] == '#' {
			client.Out <- irc.Msg{Cmd: irc.JOIN, Args: []string{args[0]}}
		} else { // private message
			getWin(args[0])
		}

	case "Nick":
		if len(args) != 1 {
			break
		}
		client.Out <- irc.Msg{Cmd: irc.NICK, Args: []string{args[0]}}

	case "Who":
		if ev.target[0] != '#' {
			break
		}
		ev.win.who = []string{}
		client.Out <- irc.Msg{Cmd: irc.WHO, Args: []string{ev.target}}

	default:
		return false
	}

	return true
}

// HandleMsg handles IRC messages from the server.
func handleMsg(msg irc.Msg) {
	switch msg.Cmd {
	case irc.ERROR:
		if !quitting {
			exit(1, "Received error: "+msg.Raw)
		}

	case irc.PING:
		client.Out <- irc.Msg{Cmd: irc.PONG}

	case irc.PONG:
		// OK, ignore

	case irc.ERR_NOSUCHNICK:
		doNoSuchNick(msg.Args[1], lastArg(msg))

	case irc.ERR_NOSUCHCHANNEL:
		doNoSuchChannel(msg.Args[1])

	case irc.RPL_MOTD:
		serverWin.WriteString(lastArg(msg))

	case irc.RPL_NAMREPLY:
		doNamReply(msg.Args[len(msg.Args)-2], lastArg(msg))

	case irc.RPL_TOPIC:
		doTopic(msg.Args[1], "", lastArg(msg))

	case irc.KICK:
		doKick(msg.Args[0], msg.Origin, msg.Args[1])

	case irc.TOPIC:
		doTopic(msg.Args[0], msg.Origin, lastArg(msg))

	case irc.MODE:
		if len(msg.Args) < 3 { // I dunno what this is, but I bet it's valid.
			cmd := irc.CmdNames[msg.Cmd]
			serverWin.WriteString("(" + cmd + ") " + msg.Raw)
			break
		}
		doMode(msg.Args[0], msg.Args[1], msg.Args[2])

	case irc.JOIN:
		doJoin(msg.Args[0], msg.Origin)

	case irc.PART:
		doPart(msg.Args[0], msg.Origin)

	case irc.QUIT:
		doQuit(msg.Origin, lastArg(msg))

	case irc.NOTICE:
		doNotice(msg.Args[0], msg.Origin, lastArg(msg))

	case irc.PRIVMSG:
		doPrivMsg(msg.Args[0], msg.Origin, msg.Args[1])

	case irc.NICK:
		doNick(msg.Origin, msg.Args[0])

	case irc.RPL_WHOREPLY:
		doWhoReply(msg.Args[1], msg.Args[2:])

	case irc.RPL_ENDOFWHO:
		doEndOfWho(msg.Args[1])

	default:
		cmd := irc.CmdNames[msg.Cmd]
		serverWin.WriteString("(" + cmd + ") " + msg.Raw)
	}
}

func doNoSuchNick(ch, msg string) {
	getWin(ch).writeMsg("=ERROR: " + ch + ":" + msg)
}

func doNoSuchChannel(ch string) {
	// Must have PARTed a channel that is not JOINed.
	getWin(ch).del()
}

func doNamReply(ch string, names string) {
	for _, n := range strings.Fields(names) {
		n = strings.TrimLeft(n, "@+")
		if n != *nick {
			doJoin(ch, n)
		}
	}
}

func doKick(ch, op, who string) {
	w := getWin(ch)
	w.writeMsg("=" + op + " kicked " + who)
	delete(w.users, who)
}

func doTopic(ch, who, what string) {
	w := getWin(ch)
	if who == "" {
		w.writeMsg("=topic: " + what)
	} else {
		w.writeMsg("=" + who + " topic: " + what)
	}
}

func doMode(ch, mode, who string) {
	if len(ch) == 0 || ch[0] != '#' {
		return
	}
	w := getWin(ch)
	w.writeMsg("=" + who + " mode " + mode)
}

func doJoin(ch, who string) {
	w := getWin(ch)
	w.writeMsg("+" + who)
	if who != *nick {
		w.users[who] = &user{
			nick:      who,
			origNick:  who,
			changedAt: time.Now(),
		}
	}
}

func doPart(ch, who string) {
	w, ok := wins[strings.ToLower(ch)]
	if !ok {
		return
	}
	if who == *nick {
		w.del()
	} else {
		w.writeMsg("-" + who)
		delete(w.users, who)
	}
}

func doQuit(who, txt string) {
	for _, w := range wins {
		if _, ok := w.users[who]; !ok {
			continue
		}
		delete(w.users, who)
		s := "-" + who + " quit"
		if txt != "" {
			s += ": " + txt
		}
		w.writeMsg(s)
	}
}

func doPrivMsg(ch, who, text string) {
	if ch == *nick {
		ch = who
	}

	if *util != "" {
		cmd := exec.Command(*util, who)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err != nil {
			log.Printf("Error running util (%s): %v\n", *util, err)
		}
	}

	// If this is NickServ, and there is no NickServ window open
	// then just dump its messages to the server window.
	l := strings.ToLower(who)
	if _, ok := wins[l]; !ok && l == strings.ToLower(nickServer) {
		serverWin.writePrivMsg(who, text)
		return
	}

	getWin(ch).writePrivMsg(who, text)
}

func doNotice(ch, who, text string) {
	doPrivMsg(ch, who, text)
}

func doNick(prev, cur string) {
	if prev == *nick {
		*nick = cur
		for _, w := range wins {
			w.writeMsg("~" + prev + " → " + cur)
		}
		return
	}

	for _, w := range wins {
		if u, ok := w.users[prev]; ok {
			delete(w.users, prev)
			u.changedAt = time.Now()
			u.nick = cur
			w.users[cur] = u
			w.writeMsg("~" + prev + " → " + cur)
		}
	}
}

func doWhoReply(ch string, info []string) {
	w := getWin(ch)
	s := info[3]
	if strings.IndexRune(info[4], '+') >= 0 {
		s = "+" + s
	}
	if strings.IndexRune(info[4], '@') >= 0 {
		s = "@" + s
	}
	w.who = append(w.who, s)
	serverWin.WriteString(ch + " " + s + " " + info[0] + "@" + info[1])
}

func doEndOfWho(ch string) {
	w := getWin(ch)
	sort.Strings(w.who)
	w.writeMsg("[" + strings.Join(w.who, "] [") + "]")
	w.who = w.who[:0]
}

// LastArg returns the last message
// argument or the empty string if there
// are no arguments.
func lastArg(msg irc.Msg) string {
	if len(msg.Args) == 0 {
		return ""
	}
	return msg.Args[len(msg.Args)-1]
}

// Exit marks all windows as clean and exits
// with the given status.
func exit(status int, why string) {
	serverWin.WriteString(why)
	serverWin.Ctl("clean")
	for _, w := range wins {
		w.WriteString(why)
		w.Ctl("clean")
	}
	os.Exit(status)
}

func username() string {
	un, err := osuser.Current()
	if err != nil {
		return ""
	}
	return un.Username
}

func name() string {
	un, err := osuser.Current()
	if err != nil || un.Name == "" {
		return un.Username
	}
	return un.Name
}

// quote returns a single-quoted string, with interior
// quotes quoted as ''.
func quote(s string) string {
	r := []byte("'")
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			r = append(r, '\'', '\'')
		} else {
			r = append(r, s[i])
		}
	}
	return string(append(r, '\''))
}
