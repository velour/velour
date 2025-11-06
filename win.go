package main

import (
	"bytes"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"9fans.net/go/acme"
	"github.com/velour/velour/irc"
)

const (
	// Prompt is the prompt string written at the beginning of the text entry line.
	prompt       = "\n>"
	promptAddr   = "$-/^>/"
	beforePrompt = promptAddr + "-#0"
	afterPrompt  = promptAddr + "+#0"

	// MeCmd is the command prefix for sending CTCP ACTIONs to a channel.
	meCmd = "/me"

	// StampTimeout is the amount of time before a time stamp is printed.
	stampTimeout = 5 * time.Minute
)

// Win is an open acme windown for either the server, a channel, or a private message.
type win struct {
	*acme.Win

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

	if who != *nick {
		re := "(\\W|^)@?" + *nick + "(\\W|$)"
		match, err := regexp.MatchString(re, text)
		if err != nil {
			fmt.Printf("regex [%s] failed: %s", re, err)
		}
		if err == nil && match {
			buf.WriteRune('!')
		}
	}
	buf.WriteRune('\t')
	buf.WriteString(text)
	return buf.String()
}

func (w *win) writeToPrompt(text string) {
	w.Addr(afterPrompt)
	w.writeData([]byte(text))
	w.Addr("%s+#%d", afterPrompt, utf8.RuneCountInString(text))
	w.Ctl("dot=addr")
}

// WriteString writes to the window's data file just before the prompt and moves prompt pointers.
func (w *win) WriteString(str string) {
	d("write string [%s]\n", str)
	w.Addr(beforePrompt)
	w.writeData([]byte(str + "\n"))
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
	w.Addr(afterPrompt + ",$")
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
		d("line=[%s]\n", t)
		w.Addr("%s,%s+#%d", beforePrompt, afterPrompt, utf8.RuneCountInString(t))
		w.send(t)
		text = text[i+1:]
	}
}

func (w *win) send(t string) {
	d("sending [%s]\n", t)
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

	// Remove a trailing newline before writing, since the prompt re-adds one.
	w.writeData([]byte(strings.TrimRight(msg, "\n") + prompt))

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

func (w *win) deleting(q0, q1 int) { w.establishPrompt() }

func (w *win) establishPrompt() {
	w.Addr(promptAddr)
	q0, q1, err := w.ReadAddr()
	if err != nil {
		panic(err)
	}
	d("prompt is at %d, %d\n", q0, q1)
	if q0 == q1 {
		// The prompt was deleted. Redraw it.
		w.Addr("$-/\\n/,$")
		q0, q1, err := w.ReadAddr()
		if err != nil {
			panic(err)
		}
		d("establishing prompt at %d, %d\n", q0, q1)
		w.Fprintf("data", prompt)
	}
}

func d(f string, v ...interface{}) {
	if *debug {
		log.Printf(f, v...)
	}
}
