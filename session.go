package pggateway

import (
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/c653labs/pgproto"
	uuid "github.com/satori/go.uuid"
)

type Session struct {
	ID       string
	User     []byte
	Database []byte

	IsSSL    bool
	client   net.Conn
	target   net.Conn
	salt     []byte
	password []byte

	startup *pgproto.StartupMessage

	stopped bool

	plugins *PluginRegistry
}

func NewSession(startup *pgproto.StartupMessage, user []byte, database []byte, isSSL bool, client net.Conn, target net.Conn, plugins *PluginRegistry) (*Session, error) {
	var err error
	id, err := uuid.NewV4()
	if err != nil {
		return nil, err
	}

	return &Session{
		ID:       id.String(),
		User:     user,
		Database: database,
		IsSSL:    isSSL,
		client:   client,
		target:   target,
		salt:     generateSalt(),
		startup:  startup,
		plugins:  plugins,
		stopped:  false,
	}, nil
}

func (s *Session) Close() {
	if s.target != nil {
		s.target.Close()
	}
}

func (s *Session) String() string {
	return fmt.Sprintf("Session<ID=%#v, User=%#v, Database=%#v>", s.ID, string(s.User), string(s.Database))
}

func (s *Session) Handle() error {
	success, err := s.plugins.Authenticate(s, s.startup)
	if err != nil {
		return err
	}

	if !success {
		errMsg := &pgproto.Error{
			Severity: []byte("Fatal"),
			Message:  []byte("failed to authenticate"),
		}
		s.WriteToClient(errMsg)
		return nil
	}

	return s.proxy()
}

func (s *Session) GetUserPassword(method pgproto.AuthenticationMethod) (*pgproto.AuthenticationRequest, *pgproto.PasswordMessage, error) {
	auth := &pgproto.AuthenticationRequest{
		Method: method,
		Salt:   s.salt,
	}
	err := s.WriteToClient(auth)
	if err != nil {
		return nil, nil, err
	}

	msg, err := s.ParseClientRequest()
	if err != nil {
		return nil, nil, err
	}

	pwdMsg, ok := msg.(*pgproto.PasswordMessage)
	if !ok {
		return nil, nil, fmt.Errorf("expected password message")
	}
	s.password = pwdMsg.Password

	return auth, pwdMsg, nil
}

func (s *Session) parseStartupMessage() (*pgproto.StartupMessage, error) {
	msg, err := s.ParseClientRequest()
	if err != nil {
		return nil, err
	}

	switch m := msg.(type) {
	case *pgproto.StartupMessage:
		// Only extract options if this isn't an SSL request
		if m.SSLRequest {
			return m, nil
		}

		var ok bool
		if s.User, ok = m.Options["user"]; !ok {
			return nil, fmt.Errorf("no username sent with startup message")
		}

		if s.Database, ok = m.Options["database"]; !ok {
			return nil, fmt.Errorf("no database name sent with startup message")
		}

		return m, nil
	}
	return nil, fmt.Errorf("unexpected message type")
}

func (s *Session) proxy() error {
	m := &sync.Mutex{}
	stop := sync.NewCond(m)
	errs := make([]error, 0)

	go s.proxyClientMessages(stop, errs)
	go s.proxyServerMessages(stop, errs)

	// Disable message interception
	// go func() {
	//	_, err := io.Copy(s.client, s.target)
	//	errs = append(errs, err)
	//	stop.Broadcast()
	// }()

	// go func() {
	//	_, err := io.Copy(s.target, s.client)
	//	errs = append(errs, err)
	//	stop.Broadcast()
	// }()

	stop.L.Lock()
	stop.Wait()
	stop.L.Unlock()
	s.stopped = true

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

func (s *Session) proxyServerMessages(stop *sync.Cond, errs []error) {
	var buf []pgproto.Message
	for !s.stopped {
		msg, err := s.ParseServerResponse()
		if err != nil {
			errs = append(errs, err)
			stop.Broadcast()
			break
		}
		buf = append(buf, msg)

		flush := false
		switch m := msg.(type) {
		case *pgproto.ReadyForQuery:
			flush = true
		case *pgproto.AuthenticationRequest:
			flush = m.Method != pgproto.AuthenticationMethodOK
		}
		if flush || len(buf) > 15 {
			pgproto.WriteMessages(buf, s.client)
			buf = nil
		}
	}
	if len(buf) > 0 {
		pgproto.WriteMessages(buf, s.client)
	}
}

func (s *Session) proxyClientMessages(stop *sync.Cond, errs []error) {
	for !s.stopped {
		msg, err := s.ParseClientRequest()
		if err != nil {
			errs = append(errs, err)
			stop.Broadcast()
			break
		}

		s.WriteToServer(msg)

		if _, ok := msg.(*pgproto.Termination); ok {
			break
		}
	}
}

func (s *Session) WriteToServer(msg pgproto.ClientMessage) error {
	_, err := pgproto.WriteMessage(msg, s.target)
	return err
}

func (s *Session) WriteToClient(msg pgproto.ServerMessage) error {
	_, err := pgproto.WriteMessage(msg, s.client)
	return err
}

func (s *Session) ParseClientRequest() (pgproto.ClientMessage, error) {
	msg, err := pgproto.ParseClientMessage(s.client)
	if err == io.EOF {
		return msg, io.EOF
	}

	if err != nil {
		if !s.stopped {
			s.plugins.LogError(s.loggingContextWithMessage(msg), "error parsing client request: %s", err)
		}
	} else {
		s.plugins.LogDebug(s.loggingContextWithMessage(msg), "client request")
	}
	return msg, err
}

func (s *Session) ParseServerResponse() (pgproto.ServerMessage, error) {
	msg, err := pgproto.ParseServerMessage(s.target)
	if err == io.EOF {
		return msg, io.EOF
	}

	if err != nil {
		if !s.stopped {
			s.plugins.LogError(s.loggingContextWithMessage(msg), "error parsing server response: %#v", err)
		}
	} else {
		s.plugins.LogDebug(s.loggingContextWithMessage(msg), "server response")
	}
	return msg, err
}

func (s *Session) loggingContext() LoggingContext {
	return LoggingContext{
		"session_id": s.ID,
		"user":       string(s.User),
		"database":   string(s.Database),
		"ssl":        s.IsSSL,
		"client":     s.client.RemoteAddr(),
		"target":     s.target.RemoteAddr(),
	}
}

func (s *Session) loggingContextWithMessage(msg pgproto.Message) LoggingContext {
	context := s.loggingContext()
	if msg != nil {
		context["message"] = msg.AsMap()
	}
	return context
}
