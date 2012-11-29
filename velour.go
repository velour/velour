// Velour is an IRC client for the acme editor.
package main

import (
	"code.google.com/p/velour/irc"
	"flag"
	"io"
	"net"
	"log"
	"os"
	osuser "os/user"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultPort is the port used to connect to
	// the server if one is not specified.
	defaultPort = "6667"

	// InitialTimeout is the initial amount of time
	// to delay before reconnecting.  Each failed
	// reconnection doubles the timout until
	// a connection is made successfully.
	initialTimeout = 2*time.Second

	// PingTime is the amount of inactivity time
	// before sending a ping to the server.
	pingTime = 120*time.Second
)

var (
	nick  = flag.String("n", username(), "nick name")
	full  = flag.String("f", name(), "full name")
	pass  = flag.String("p", "", "password")
	debug = flag.Bool("d", false, "debugging")
)

var (
	// client is the IRC client connection.
	client *irc.Client

	// Server is the server's address.
	server = ""

	// serverWin is the server win.
	serverWin *win

	// wins contains the wins, indexed by their targets.
	wins = map[string]*win{}

	// winEvents multiplexes all win events.
	winEvents = make(chan winEvent)

	// users contains all known users.
	users = map[string]*user{}

	// Quitting is set to true if the user Dels
	// the server window.
	quitting = false
)

func main() {
	flag.Usage = func() {
		os.Stdout.WriteString("usage: velour [options] <server>[:<port>]\n")
		flag.PrintDefaults()
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

	serverWin = newWindow("")
	serverWin.Fprintf("tag", "Chat ")
	// Set Dump handling for the server window.
	if wd, err := os.Getwd(); err != nil {
		log.Println("Failed to set dump working directory: " + err.Error())
	} else {
		serverWin.Ctl("dumpdir %s", wd)
		serverWin.Ctl("dump %s", strings.Join(os.Args, " "))
	}

	for {
		handleConnecting(connect(server + ":" + port))

		serverWin.WriteString("Connected")
		for _, w := range wins {
			w.WriteString("Connected")
			if len(w.target) > 0 && w.target[0] == '#' {
				client.Out <- irc.Msg{Cmd: irc.JOIN, Args: []string{w.target}}
			}
		}

		handleConnection()

		if quitting {
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
			client, err = irc.DialServer(addr, *nick, *full, *pass)
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
			handleError(err)
		}
	}()

	for {
		select {
		case ev := <-winEvents:
			handleWindowEvent(ev)

		case msg, ok := <-client.In:
			if !ok { // disconnect
				return
			}
			t.Stop()
			t = time.NewTimer(pingTime)
			handleMsg(msg)

		case <- t.C:
			client.Out <- irc.Msg{Cmd:irc.PING, Args: []string{client.Server}}
			t = time.NewTimer(pingTime)

		case err, ok := <-client.Errors:
			if ok {
				handleError(err)
			}
		}
	}
}

func handleError(err error) {
	if err == io.EOF {
		return
	}
	if l, ok := err.(irc.MsgTooLong); ok {
		log.Println(l.Error())
		return
	}
	exit(1, err.Error())
}

// HandleWindowEvent handles events from
// any of the acme wins.
func handleWindowEvent(ev winEvent) {
	if *debug {
		log.Printf("%#v\n\n", *ev.Event)
	}

	switch {
	case ev.C2 == 'x' || ev.C2 == 'X':
		text := strings.TrimSpace(string(ev.Text))
		if users[text] != nil {
			name := text + ", "
			ev.Addr("#%d", ev.eAddr)
			ev.writeData([]byte(name))
			ev.Addr("#%d", ev.eAddr + utf8.RuneCountInString(name))
			ev.Ctl("dot=addr")
			ev.Addr("#%d", ev.pAddr)
			return
		}
		fs := strings.Fields(text)
		if len(fs) > 0 && handleExecute(ev, fs[0], fs[1:]) {
			return
		}
		if ev.Flag & 2 != 0 {
			ev.Q0 = ev.OrigQ0
			ev.Q1 = ev.OrigQ1
		}
		ev.WriteEvent(ev.Event)

	case (ev.C1 == 'M' || ev.C1 == 'K') && ev.C2 == 'I':
		ev.typing(ev.Q0, ev.Q1)

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
			getWindow(args[0])
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
	if *debug {
		log.Printf("%#v\n\n", msg)
	}

	switch msg.Cmd {
	case irc.ERROR:
		if !quitting {
			exit(1, "Received error: " + msg.Raw)
		}

	case irc.PING:
		client.Out <- irc.Msg{Cmd: irc.PONG}

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
	getWindow(ch).writeMsg("=ERROR: " + ch + ":" + msg)
}

func doNoSuchChannel(ch string) {
	// Must have PARTed a channel that is not JOINed.
	getWindow(ch).del()
}

func doNamReply(ch string, names string) {
	for _, n := range strings.Fields(names) {
		n = strings.TrimLeft(n, "@+")
		if n != *nick {
			doJoin(ch, n)
		}
	}
}

func doTopic(ch, who, what string) {
	w := getWindow(ch)
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
	w := getWindow(ch)
	w.writeMsg("=" + who + " mode " + mode)
}

func doJoin(ch, who string) {
	w := getWindow(ch)
	w.writeMsg("+" + who)
	if who != *nick {
		u := getUser(who)
		w.users[who] = u
		u.nChans++
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
		u := getUser(who)
		u.nChans--
		if u.nChans == 0 {
			delete(users, who)
		}
	}
}

func doQuit(who, txt string) {
	delete(users, who)
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
	getWindow(ch).writePrivMsg(who, text)
}

func doNotice(ch, who, text string) {
	doPrivMsg(ch, who, text)
}

func doNick(prev, cur string) {
	if prev == *nick {
		*nick = cur
		for _, w := range wins {
			w.writeMsg("~" + prev + " -> " + cur)
		}
		return
	}

	u := users[prev]
	delete(users, prev)
	users[cur] = u
	u.nick = cur
	u.lastChange = time.Now()

	for _, w := range wins {
		if _, ok := w.users[prev]; !ok {
			continue
		}
		delete(w.users, prev)
		w.users[cur] = u
		w.writeMsg("~" + prev + " -> " + cur)
	}
}

func doWhoReply(ch string, info []string) {
	w := getWindow(ch)
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
	w := getWindow(ch)
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
	if err != nil {
		return ""
	}
	return un.Name
}
