package irc

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"time"
)

// A Client is a client's connection to an IRC server.
type Client struct {
	conn net.Conn

	// Server is the server to which the client
	// is connected.
	Server string

	// In is a channel of all incoming messages
	// from the server.
	In <-chan Msg

	// Msgs sent to Out are written to the server.
	Out chan<- Msg

	// Errors is a channel of all read or write errors.
	Errors <-chan error
}

// Dial connects to a remote IRC server.
func Dial(server, nick, fullname, pass string) (*Client, error) {
	c, err := net.Dial("tcp", server)
	if err != nil {
		return nil, err
	}
	return dial(c, nick, fullname, pass)
}

// DialSSL connects to a remote IRC server using SSL.
func DialSSL(server, nick, fullname, pass string, trust bool) (*Client, error) {
	c, err := tls.Dial("tcp", server, &tls.Config{InsecureSkipVerify: trust})
	if err != nil {
		return nil, err
	}
	return dial(c, nick, fullname, pass)
}

func dial(conn net.Conn, nick, fullname, pass string) (*Client, error) {
	messagesIn := make(chan Msg, 0)
	messagesOut := make(chan Msg, 0)
	errChan := make(chan error)
	c := &Client{
		conn:   conn,
		In:     messagesIn,
		Out:    messagesOut,
		Errors: errChan,
	}

	readErrs := make(chan error)
	go c.readMsgs(readErrs, messagesIn)

	writeErrs := make(chan error)
	go c.writeMsgs(writeErrs, messagesOut)

	go c.muxErrors(readErrs, writeErrs, errChan)

	return c, c.register(nick, fullname, pass)
}

// register registers a name with the server
func (c *Client) register(nick, fullname, pass string) error {
	if pass != "" {
		c.Out <- Msg{
			Cmd:  "PASS",
			Args: []string{pass},
		}
	}
	c.Out <- Msg{
		Cmd:  "NICK",
		Args: []string{nick},
	}
	c.Out <- Msg{
		Cmd:  "USER",
		Args: []string{nick, "0", "*", fullname},
	}
	for msg := range c.In {
		switch msg.Cmd {
		case ERR_NONICKNAMEGIVEN, ERR_ERRONEUSNICKNAME,
			ERR_NICKNAMEINUSE, ERR_NICKCOLLISION,
			ERR_UNAVAILRESOURCE, ERR_RESTRICTED,
			ERR_NEEDMOREPARAMS, ERR_ALREADYREGISTRED:
			if len(msg.Args) > 0 {
				return errors.New(msg.Args[len(msg.Args)-1])
			}
			return errors.New(CmdNames[msg.Cmd])

		case RPL_WELCOME:
			c.Server = msg.Origin
			return nil

		case PING:
			// Some IRC servers (UnrealIRCd) use PING-based IP
			// spoofing prevention: they send a PING with some
			// junk and  require the client to reply with a PONG
			// that include the same junk. If the client is an IP
			// spoofer, they cannot see the PING, and thus cannot
			// reply with the same junk. We aren't spoofing, so
			// lets give them the junk that they want.
			c.Out <- Msg{Cmd: PONG, Args: msg.Args}

		default:
			/* ignore */
		}
	}
	return errors.New("unexpected end of file")
}

const deadline = 1 * time.Minute

// readMsgs reads messages from the client and
// sends them on the message channel.  If an
// error occurs then it is sent on the errs channel,
// the errs channel, ms channel, and connection
// are all closed and the routine terminates.
func (c *Client) readMsgs(errs chan<- error, ms chan<- Msg) {
	in := bufio.NewReader(c.conn)
	for {
		m, err := readMsg(in)
		if err != nil {
			errs <- err
			if _, ok := err.(MsgTooLong); !ok {
				break
			}
		}
		ms <- m
	}
	close(errs)
	close(ms)
	c.conn.Close()
}

// writeMsgs writes the messages coming in on the
// channel to the connection.  If there is an error,
// it is sent on the errs channel.  If the error occurs
// while writing to the client then the routine
// closes the errs channel, the connection, and
// discards all remaining messages.
func (c *Client) writeMsgs(errs chan<- error, ms <-chan Msg) {
	out := bufio.NewWriter(c.conn)
	for m := range ms {
		str, err := m.RawString()
		if err != nil {
			errs <- err
			continue
		}
		if _, err = out.WriteString(str + "\r\n"); err != nil {
			errs <- err
			break
		}
		c.conn.SetWriteDeadline(time.Now().Add(deadline))
		if err = out.Flush(); err != nil {
			errs <- err
			break
		}
	}
	close(errs)
	c.conn.Close()

	// Junk the remaining messages.
	for _ = range ms {
	}
}

// muxErrors multiplexes read and write errors
// to the error channel.
func (c *Client) muxErrors(rerrs <-chan error, werrs <-chan error, errs chan<- error) {
	left := 2
	for {
		select {
		case err, ok := <-rerrs:
			if ok {
				errs <- err
				continue
			}
			left--
			if left == 0 {
				close(errs)
				return
			}

		case err, ok := <-werrs:
			if ok {
				errs <- err
				continue
			}
			left--
			if left == 0 {
				close(errs)
				return
			}
		}
	}
}
