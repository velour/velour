package main

import (
	"bytes"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"9fans.net/go/acme"
	"github.com/velour/velour/irc"
)

const (
	// Prompt is the prompt string written at the beginning of the text entry line.
	prompt = "\n>"

	// MeCmd is the command prefix for sending CTCP ACTIONs to a channel.
	meCmd = "/me"

	// StampTimeout is the amount of time before a time stamp is printed.
	stampTimeout = 5 * time.Minute
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

type user struct {
	nick      string
	origNick  string
	changedAt time.Time
}

type winEvent struct {
	// TimeStamp is set to true for time stamp events. If timeStamp is true then Event is nil.
	timeStamp bool

	*win
	*acme.Event
}

func newWin(target string) *win {
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

func (w *win) del() {
	if w.stampTimer != nil {
		w.stampTimer.Stop()
	}
	delete(wins, strings.ToLower(w.target))
	w.Ctl("delete")
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

		if u, ok := w.users[who]; ok {
			// If the user hasn't change their nick in an hour
			// then set this as the original nick name.
			if time.Since(u.changedAt).Hours() > 1 {
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
	if *debug {
		defer func(p, e int) {
			w.Addr("#%d", w.eAddr)
			text, err := w.ReadAll("data")
			if err != nil {
				panic(err)
			}
			w.Addr("#%d", w.pAddr)
			log.Printf("typing pAddr before: %d, pAddr after: %d, eAddr before: %d, eAddr after: %d [%s]\n", p, w.pAddr, e, w.eAddr, text)
		}(w.pAddr, w.eAddr)
	}

	if q0 < w.pAddr {
		d("typing before prompt")
		w.pAddr += q1 - q0
	}
	if q0 < w.eAddr {
		d("typing before entry")
		w.eAddr += q1 - q0
		return
	}
	if q0 < w.pAddr {
		return
	}

	defer w.Addr("#%d", w.pAddr)

	w.Addr("#%d", w.eAddr)
	text, err := w.ReadAll("data")
	if err != nil {
		panic("Failed to read from window: " + err.Error())
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
	defer w.Addr("#%d", w.pAddr)

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
		for len(t) > 0 {
			m := irc.Msg{
				Cmd:  "PRIVMSG",
				Args: []string{w.target, t},
			}
			_, err := m.RawString()
			if err != nil {
				mtl := err.(irc.MsgTooLong)
				m.Args[1] = t[:mtl.NTrunc]
				t = t[mtl.NTrunc:]
			} else {
				t = ""
			}
			client.Out <- m
		}
	}
}

func (w *win) deleting(q0, q1 int) {
	if *debug {
		defer func(p, e int) {
			w.Addr("#%d", w.eAddr)
			text, err := w.ReadAll("data")
			if err != nil {
				log.Printf("Entry address (%d) is out of range!", w.eAddr)
			}
			w.Addr("#%d", w.pAddr)
			log.Printf("deleting: pAddr before: %d, pAddr after: %d, eAddr before: %d, eAddr after: %d [%s]\n", p, w.pAddr, e, w.eAddr, text)
		}(w.pAddr, w.eAddr)
	}

	if q0 > w.eAddr {
		d("deleting after entry")
		return
	}

	if q1 >= w.eAddr {
		d("deleting over entry\n")
		w.eAddr = q0
	} else {
		d("deleting before entry\n")
		w.eAddr -= q1 - q0
	}

	if q0 > w.pAddr {
		d("deleting after prompt")
		return
	}

	if q1 >= w.pAddr {
		d("deleting over prompt\n")
		w.pAddr = q0
	} else if q0 < w.pAddr {
		d("deleting before prompt\n")
		w.pAddr -= q1 - q0
	}
}

func d(f string, v ...interface{}) {
	if *debug {
		log.Printf(f, v...)
	}
}
