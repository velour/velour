package main

import (
	"bytes"
	"code.google.com/p/goplan9/plan9/acme"
	"code.google.com/p/velour/irc"
	"log"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// Prompt is the prompt string written at the beginning of the text entry line.
	prompt = "\n>"

	// MeCmd is the command prefix for sending CTCP ACTIONs to a channel.
	meCmd = "/me"

	// StampTimeout is the amount of time before a time stamp is printed.
	stampTimeout = 2 * time.Minute
)

// Win is an open acme windown for either the server, a channel, or a private message.
type win struct {
	*acme.Win

	// PAddr is the address of the empty string just before the prompt.
	// EAddr is the address of the empty string just after the prompt and before
	// the user's input.
	pAddr, eAddr int

	// channel name or nick of chatter for this window.
	target string

	// Who is a list of users gathered by a who command.
	who []string

	users       map[string]*user
	lastSpeaker string
	lastTime    time.Time
	stampTimer  *time.Timer
}

type winEvent struct {
	// TimeStamp is set to true for time stamp events. If timeStamp is true then Event is nil.
	timeStamp bool

	*win
	*acme.Event
}

func newWindow(target string) *win {
	aw, err := acme.New()
	if err != nil {
		panic("Failed to create window: " + err.Error())
	}
	name := "/irc/" + server
	if target != "" {
		name += "/" + target
	}
	aw.Name(name)
	aw.Ctl("clean")
	aw.Write("body", []byte(prompt))
	if len(target) > 0 && target[0] == '#' {
		aw.Fprintf("tag", "Who ")
	}

	w := &win{
		Win:      aw,
		eAddr:    utf8.RuneCountInString(prompt),
		target:   target,
		users:    make(map[string]*user),
		lastTime: time.Now(),
	}
	go func() {
		for ev := range aw.EventChan() {
			winEvents <- winEvent{false, w, ev}
		}
	}()
	return w
}

func (w *win) writeMsg(text string) {
	w.WriteString(text)
	w.lastSpeaker = ""
}

func (w *win) writePrivMsg(who, text string) {
	s := w.privMsgString(who, text)
	if *debug {
		log.Printf("msg string=[%s]\nnum runes=%d\n", s,
			utf8.RuneCountInString(s))
	}
	w.WriteString(s)
}

const actionPrefix = "\x01ACTION"

func (w *win) privMsgString(who, text string) string {
	if text == "\n" {
		return ""
	}

	if strings.HasPrefix(text, actionPrefix) {
		text = strings.TrimRight(text[len(actionPrefix):], "\x01")
		if w.lastSpeaker != who {
			w.lastSpeaker = ""
		}
		w.lastTime = time.Now()
		return "*" + who + text
	}

	buf := bytes.NewBuffer(make([]byte, 0, 512))

	// Only print the user name if there is a new speaker or if two minutes has passed.
	if who != w.lastSpeaker {
		buf.WriteRune('<')
		buf.WriteString(who)
		buf.WriteRune('>')

		if u, ok := users[who]; ok {
			// If the user hasn't change their nick in an hour
			// then set this as the original nick name.
			if time.Since(u.lastChange).Hours() > 1 {
				u.origNick = u.nick
			}
			if u.nick != u.origNick {
				buf.WriteString(" (")
				buf.WriteString(u.origNick)
				buf.WriteRune(')')
			}
		}
		buf.WriteRune('\n')
	}
	w.lastSpeaker = who
	w.lastTime = time.Now()
	if w.stampTimer != nil {
		w.stampTimer.Stop()
	}
	w.stampTimer = time.AfterFunc(stampTimeout, func() {
		winEvents <- winEvent{true, w, nil}
	})

	if who != *nick && strings.Contains(text, *nick) {
		buf.WriteRune('!')
	}
	buf.WriteRune('\t')
	buf.WriteString(text)
	return buf.String()
}

func (w *win) writeToPrompt(text string) {
	w.Addr("#%d", w.eAddr)
	w.writeData([]byte(text))
	w.Addr("#%d", w.eAddr+utf8.RuneCountInString(text))
	w.Ctl("dot=addr")
	w.Addr("#%d", w.pAddr)
}

// WriteString writes to the window's data file just before the prompt and moves prompt pointers.
func (w *win) WriteString(str string) {
	w.Addr("#%d", w.pAddr)
	data := []byte(str + "\n")
	w.writeData(data)

	nr := utf8.RuneCount(data)
	w.pAddr += nr
	w.eAddr += nr
}

// WriteData writes to the window data file, doesn't move the prompt pointers.
func (w *win) writeData(data []byte) {
	const maxWrite = 512
	for len(data) > 0 {
		sz := len(data)
		if sz > maxWrite {
			sz = maxWrite
		}
		n, err := w.Write("data", data[:sz])
		if err != nil {
			panic("Failed to write to window: " + err.Error())
		}
		data = data[n:]
	}
}

func (w *win) printTimeStamp() {
	w.lastSpeaker = ""
	w.WriteString(w.lastTime.Format("[15:04:06]"))
}

func (w *win) typing(q0, q1 int) {
	if q0 < w.pAddr {
		w.pAddr += q1 - q0
		// w.eAddr ≥ w.pAddr so this
		// call returns in the next if clause.
	}
	if q1 < w.eAddr {
		w.eAddr += q1 - q0
		return
	}

	defer w.Addr("#%d", w.pAddr)

	w.Addr("#%d", w.eAddr)
	text, err := w.ReadAll("data")
	if err != nil {
		panic("Failed to read from window: " + err.Error())
	}

	if *debug {
		log.Printf("typing:\n\t[%s]\n\tpAddr=%d\n\teAddr=%d\n\n",
			text, w.pAddr, w.eAddr)
	}
	// If the last character after the prompt isn't a newline then
	// wait.  This fixes a bug where Send sends two typing
	// events, the sent text and a new line.  The text won't
	// be issued to w.send() until the final newline is received.
	// Otherwise the first insert event messes up the
	// addresses and the subsequent event (with the newline)
	// appears to have inserted a newline before pAddr.
	if r, _ := utf8.DecodeLastRune(text); r != '\n' {
		return
	}
	for {
		i := bytes.IndexRune(text, '\n')
		if i < 0 {
			break
		}

		t := string(text[:i+1])
		w.Addr("#%d,#%d", w.pAddr, w.eAddr+utf8.RuneCountInString(t))
		w.send(t)
		text = text[i+1:]
	}
}

func (w *win) send(t string) {
	if len(t) > 0 && t[len(t)-1] != '\n' {
		t = t + "\n"
	}
	if strings.HasPrefix(t, meCmd) {
		act := strings.TrimLeft(t[len(meCmd):], " \t")
		act = strings.TrimRight(act, "\n")
		if act == "\n" {
			t = "\n"
		} else {
			t = actionPrefix + " " + act + "\x01"
		}
	}

	msg := ""
	if w == serverWin {
		if msg = t; msg == "\n" {
			msg = ""
		}
	} else {
		msg = w.privMsgString(*nick, t)

		// Always tack on a newline.
		// In the case of a /me command, the
		// newline will be missing, it is added
		// here.
		if len(msg) > 0 && msg[len(msg)-1] != '\n' {
			msg = msg + "\n"
		}
	}
	w.writeData([]byte(msg + prompt))

	w.pAddr += utf8.RuneCountInString(msg)
	w.eAddr = w.pAddr + utf8.RuneCountInString(prompt)

	if *debug {
		log.Printf("sent:\n\t[%s]\n\tnum runes=%d\n\tpAddr=%d\n\teAddr=%d\n\n",
			msg, utf8.RuneCountInString(msg), w.pAddr, w.eAddr)
	}

	if t == "\n" {
		return
	}
	if w == serverWin {
		t = strings.TrimLeft(t, " \t")
		if msg, err := irc.ParseMsg(t); err != nil {
			log.Println("Failed to parse message: " + err.Error())
		} else {
			client.Out <- msg
		}
	} else {
		// BUG(eaburns): Long PRIVMSGs should be broken up and sent in pieces.
		client.Out <- irc.Msg{
			Cmd:  "PRIVMSG",
			Args: []string{w.target, t},
		}
	}
	w.Addr("#%d", w.pAddr)
}

func (w *win) deleting(q0, q1 int) {
	p := w.pAddr

	if q0 >= w.eAddr { // Deleting entirely after the entry point.
		return
	}
	if q1 >= w.eAddr {
		w.eAddr = q0
	} else {
		w.eAddr -= q1 - q0
	}
	if q0 < w.pAddr {
		if q1 >= w.pAddr {
			w.pAddr = q0
		} else {
			w.pAddr -= q1 - q0
		}
	}

	if q1 <= p { // Deleted entirely before the prompt
		return
	}

	// Don't redraw the prompt if there is more than a single
	// deleted rune.  The prompt must have been hilighted, and
	// if the delete was caused by text being entered then the
	// redraw will muck up the addresses for the subsequent
	// typing event.
	if q1 > q0+1 {
		return
	}

	w.Addr("#%d,#%d", w.pAddr, w.eAddr)
	w.writeData([]byte(prompt))
	w.eAddr = w.pAddr + utf8.RuneCountInString(prompt)
}

func (w *win) del() {
	if w.stampTimer != nil {
		w.stampTimer.Stop()
	}
	delete(wins, strings.ToLower(w.target))
	w.Ctl("delete")
}

type user struct {
	nick     string
	origNick string

	// LastChange is the time at which the user last changed their nick.
	lastChange time.Time

	// NChans is the reference count on the number of channel windows
	// containing this same user.
	nChans int
}
