package smtpd

import (
	"errors"

	"github.com/emersion/go-sasl"
)

// loginServer implements the server half of AUTH LOGIN.
//
// go-sasl ships LOGIN as a client only -- there is a NewLoginClient but no
// NewLoginServer, unlike PLAIN. The mechanism is not in any RFC, but
// PHPMailer and the WordPress SMTP plugins reach for it before PLAIN, so
// without this a large share of the tools people point at Mailman would fail
// to authenticate.
//
// The exchange is two challenges: the server asks for a username, then a
// password, each base64-encoded on the wire. The exact challenge strings
// matter -- go-sasl's client compares them byte for byte and errors on
// anything else.
type loginServer struct {
	authenticate func(username, password string) error

	username string
	step     int
}

func newLoginServer(authenticate func(username, password string) error) sasl.Server {
	return &loginServer{authenticate: authenticate}
}

// Next drives the exchange one step at a time.
//
// The step counter is what tracks position, deliberately rather than testing
// whether the username is empty: a client is free to send an empty username,
// and treating that as "not asked yet" re-prompts for a password forever
// until the read timeout fires.
func (s *loginServer) Next(response []byte) (challenge []byte, done bool, err error) {
	switch s.step {
	case 0:
		// A bare "AUTH LOGIN" arrives with no response at all and needs
		// prompting. A client that sent "AUTH LOGIN <base64-username>"
		// arrives here with the username already in hand.
		if response == nil {
			return []byte("Username:"), false, nil
		}
		s.username = string(response)
		s.step = 1
		return []byte("Password:"), false, nil

	case 1:
		s.step = 2
		return nil, true, s.authenticate(s.username, string(response))

	default:
		return nil, false, errors.New("sasl: unexpected client response after LOGIN completed")
	}
}
