package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

const appServerTunnelTTL = 45 * time.Second

type appServerTunnelBroker struct {
	mu       sync.Mutex
	sessions map[uuid.UUID]*appServerTunnelSession
	tickets  map[string]uuid.UUID
	pending  map[uuid.UUID]chan uuid.UUID
}

type appServerTunnelSession struct {
	id             uuid.UUID
	workerID       uuid.UUID
	surface        workerprotocol.AppServerTunnelSurface
	expiresAt      time.Time
	ticket         string
	claimed        bool
	mobileReserved bool
	workerReserved bool
	mobile         tunnelMessageTransport
	worker         tunnelMessageTransport
	done           chan struct{}
	startOnce      sync.Once
	closeOnce      sync.Once
}

type tunnelMessage struct {
	messageType int
	payload     []byte
}

type tunnelMessageTransport interface {
	ReadMessage() (int, []byte, error)
	WriteMessage(int, []byte) error
	Close() error
}

type memoryMessageTransport struct {
	incoming <-chan tunnelMessage
	outgoing chan<- tunnelMessage
	done     chan struct{}
	close    *sync.Once
}

func newMemoryMessagePipe() (*memoryMessageTransport, *memoryMessageTransport) {
	leftToRight := make(chan tunnelMessage, 128)
	rightToLeft := make(chan tunnelMessage, 128)
	done := make(chan struct{})
	closeOnce := &sync.Once{}
	left := &memoryMessageTransport{incoming: rightToLeft, outgoing: leftToRight,
		done: done, close: closeOnce}
	right := &memoryMessageTransport{incoming: leftToRight, outgoing: rightToLeft,
		done: done, close: closeOnce}
	return left, right
}

func (t *memoryMessageTransport) ReadMessage() (int, []byte, error) {
	select {
	case message := <-t.incoming:
		return message.messageType, message.payload, nil
	case <-t.done:
		return 0, nil, net.ErrClosed
	}
}

func (t *memoryMessageTransport) WriteMessage(messageType int, payload []byte) error {
	message := tunnelMessage{messageType: messageType, payload: append([]byte(nil), payload...)}
	select {
	case t.outgoing <- message:
		return nil
	case <-t.done:
		return net.ErrClosed
	}
}

func (t *memoryMessageTransport) Close() error {
	t.close.Do(func() { close(t.done) })
	return nil
}

func newAppServerTunnelBroker() *appServerTunnelBroker {
	return &appServerTunnelBroker{sessions: make(map[uuid.UUID]*appServerTunnelSession),
		tickets: make(map[string]uuid.UUID), pending: make(map[uuid.UUID]chan uuid.UUID)}
}

func (b *appServerTunnelBroker) issue(workerID uuid.UUID,
	surface workerprotocol.AppServerTunnelSurface,
) (string,
	*appServerTunnelSession, error,
) {
	if surface != workerprotocol.AppServerTunnelSurfaceControl &&
		surface != workerprotocol.AppServerTunnelSurfaceMobile {
		return "", nil, errors.New("app server 隧道 surface 无效")
	}
	ticket, err := security.RandomToken(32)
	if err != nil {
		return "", nil, err
	}
	session := &appServerTunnelSession{id: uuid.New(), workerID: workerID, surface: surface,
		expiresAt: time.Now().UTC().Add(appServerTunnelTTL), ticket: ticket,
		done: make(chan struct{})}
	b.mu.Lock()
	queue := b.pending[workerID]
	if queue == nil {
		queue = make(chan uuid.UUID, 128)
		b.pending[workerID] = queue
	}
	b.sessions[session.id] = session
	b.tickets[ticket] = session.id
	b.mu.Unlock()
	select {
	case queue <- session.id:
		go b.expire(session)
		return ticket, session, nil
	default:
		b.finish(session)
		return "", nil, errors.New("worker 隧道队列已满")
	}
}

// issueSystem 为 Control 的 Workspace 官方连接创建内存端点；Worker 一侧仍使用
// 与移动端相同的反向 WebSocket，JSON-RPC 消息不会被包装或改写。
func (b *appServerTunnelBroker) issueSystem(workerID uuid.UUID) (tunnelMessageTransport,
	*appServerTunnelSession, error,
) {
	ticket, session, err := b.issue(workerID, workerprotocol.AppServerTunnelSurfaceControl)
	if err != nil {
		return nil, nil, err
	}
	b.mu.Lock()
	delete(b.tickets, ticket)
	session.mobileReserved = true
	b.mu.Unlock()
	client, broker := newMemoryMessagePipe()
	if err := b.bind(session, broker, true); err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	return client, session, nil
}

func (b *appServerTunnelBroker) claim(ctx context.Context,
	workerID uuid.UUID,
) (*appServerTunnelSession, error) {
	b.mu.Lock()
	queue := b.pending[workerID]
	if queue == nil {
		queue = make(chan uuid.UUID, 128)
		b.pending[workerID] = queue
	}
	b.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case id := <-queue:
			b.mu.Lock()
			session := b.sessions[id]
			if session != nil && time.Now().Before(session.expiresAt) && !session.claimed {
				session.claimed = true
				b.mu.Unlock()
				return session, nil
			}
			b.mu.Unlock()
		}
	}
}

func (b *appServerTunnelBroker) mobile(ticket string) (*appServerTunnelSession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id, ok := b.tickets[ticket]
	session := b.sessions[id]
	if !ok || session == nil || time.Now().After(session.expiresAt) || session.mobileReserved {
		return nil, errors.New("隧道 ticket 无效或已使用")
	}
	delete(b.tickets, ticket)
	session.mobileReserved = true
	return session, nil
}

func (b *appServerTunnelBroker) worker(id, workerID uuid.UUID) (*appServerTunnelSession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	session := b.sessions[id]
	if session == nil || session.workerID != workerID || !session.claimed ||
		time.Now().After(session.expiresAt) || session.workerReserved {
		return nil, errors.New("worker 隧道无效或已连接")
	}
	session.workerReserved = true
	return session, nil
}

func (b *appServerTunnelBroker) bind(session *appServerTunnelSession,
	connection tunnelMessageTransport, mobile bool,
) error {
	b.mu.Lock()
	if b.sessions[session.id] != session {
		b.mu.Unlock()
		return errors.New("隧道已经关闭")
	}
	if mobile {
		if !session.mobileReserved || session.mobile != nil {
			b.mu.Unlock()
			return errors.New("移动端隧道已经绑定")
		}
		session.mobile = connection
	} else {
		if !session.workerReserved || session.worker != nil {
			b.mu.Unlock()
			return errors.New("worker 隧道已经绑定")
		}
		session.worker = connection
	}
	ready := session.mobile != nil && session.worker != nil
	mobileConnection, workerConnection := session.mobile, session.worker
	b.mu.Unlock()
	if ready {
		session.startOnce.Do(func() {
			go func() {
				relayWebSocketMessages(mobileConnection, workerConnection)
				b.finish(session)
			}()
		})
	}
	return nil
}

func (b *appServerTunnelBroker) expire(session *appServerTunnelSession) {
	timer := time.NewTimer(time.Until(session.expiresAt))
	defer timer.Stop()
	<-timer.C
	b.mu.Lock()
	active := session.mobile != nil && session.worker != nil
	b.mu.Unlock()
	if !active {
		b.finish(session)
	}
}

func (b *appServerTunnelBroker) finish(session *appServerTunnelSession) {
	session.closeOnce.Do(func() {
		b.mu.Lock()
		delete(b.sessions, session.id)
		delete(b.tickets, session.ticket)
		mobile, worker := session.mobile, session.worker
		close(session.done)
		b.mu.Unlock()
		if mobile != nil {
			_ = mobile.Close()
		}
		if worker != nil {
			_ = worker.Close()
		}
	})
}

const (
	appServerTunnelPingInterval = 5 * time.Second
	appServerTunnelPongTimeout  = 15 * time.Second
)

type tunnelKeepaliveTransport interface {
	SetReadDeadline(time.Time) error
	SetPongHandler(func(string) error)
	WriteControl(int, []byte, time.Time) error
}

func relayWebSocketMessages(left, right tunnelMessageTransport) {
	relayWebSocketMessagesWithKeepalive(left, right, appServerTunnelPingInterval,
		appServerTunnelPongTimeout)
}

func relayWebSocketMessagesWithKeepalive(left, right tunnelMessageTransport,
	pingInterval, pongTimeout time.Duration,
) {
	failure := make(chan struct{}, 3)
	copyDone := make(chan struct{}, 2)
	stopKeepalive := make(chan struct{})
	keepalive, hasKeepalive := right.(tunnelKeepaliveTransport)
	if hasKeepalive {
		_ = keepalive.SetReadDeadline(time.Now().Add(pongTimeout))
		keepalive.SetPongHandler(func(string) error {
			return keepalive.SetReadDeadline(time.Now().Add(pongTimeout))
		})
	}
	copyMessages := func(destination, source tunnelMessageTransport) {
		defer func() {
			failure <- struct{}{}
			copyDone <- struct{}{}
		}()
		for {
			messageType, payload, err := source.ReadMessage()
			if err != nil {
				return
			}
			if err := destination.WriteMessage(messageType, payload); err != nil {
				return
			}
		}
	}
	go copyMessages(right, left)
	go copyMessages(left, right)
	if hasKeepalive {
		go func() {
			ticker := time.NewTicker(pingInterval)
			defer ticker.Stop()
			for {
				select {
				case <-stopKeepalive:
					return
				case <-ticker.C:
					if err := keepalive.WriteControl(websocket.PingMessage, nil,
						time.Now().Add(pingInterval)); err != nil {
						failure <- struct{}{}
						return
					}
				}
			}
		}()
	}
	<-failure
	close(stopKeepalive)
	_ = left.Close()
	_ = right.Close()
	<-copyDone
	<-copyDone
}

func (s *Server) clientCreateAppServerTunnel(c *gin.Context) {
	var request struct {
		WorkspaceID uuid.UUID `json:"workspaceId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	var workerID uuid.UUID
	err := s.db.QueryRowContext(c.Request.Context(), `SELECT workspace.worker_id
		FROM worker_workspaces workspace JOIN workers worker ON worker.id=workspace.worker_id
		WHERE workspace.id=$1 AND worker.enabled=true`, request.WorkspaceID).Scan(&workerID)
	if err != nil {
		problem(c, http.StatusConflict, "Workspace 没有可用 Worker", err)
		return
	}
	ticket, session, err := s.appServerTunnels.issue(workerID,
		workerprotocol.AppServerTunnelSurfaceMobile)
	if err != nil {
		problem(c, http.StatusServiceUnavailable, "创建 App Server 隧道失败", err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"ticket": ticket, "tunnelId": session.id,
		"expiresAt":     session.expiresAt,
		"websocketPath": "/api/v1/client/tunnels/" + ticket + "/connect"})
}

func (s *Server) workerClaimAppServerTunnel(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	session, err := s.appServerTunnels.claim(ctx, currentWorker(c).ID)
	if err != nil {
		c.JSON(http.StatusOK, workerprotocol.AppServerTunnelClaimResponse{})
		return
	}
	c.JSON(http.StatusOK, workerprotocol.AppServerTunnelClaimResponse{
		Tunnel: &workerprotocol.AppServerTunnelClaim{ID: session.id, ExpiresAt: session.expiresAt,
			Surface: session.surface},
	})
}

func (s *Server) clientConnectAppServerTunnel(c *gin.Context) {
	session, err := s.appServerTunnels.mobile(c.Param("ticket"))
	if err != nil {
		problem(c, http.StatusUnauthorized, "App Server tunnel ticket 无效", err)
		return
	}
	s.upgradeAppServerTunnel(c, session, true)
}

func (s *Server) workerConnectAppServerTunnel(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	session, err := s.appServerTunnels.worker(id, currentWorker(c).ID)
	if err != nil {
		problem(c, http.StatusConflict, "Worker App Server 隧道无效", err)
		return
	}
	s.upgradeAppServerTunnel(c, session, false)
}

func (s *Server) upgradeAppServerTunnel(c *gin.Context, session *appServerTunnelSession,
	mobile bool,
) {
	upgrader := websocket.Upgrader{HandshakeTimeout: 5 * time.Second,
		CheckOrigin: func(request *http.Request) bool {
			origin := request.Header.Get("Origin")
			if origin == "" {
				return true
			}
			publicURL, err := url.Parse(s.cfg.PublicURL)
			return err == nil && origin == publicURL.Scheme+"://"+publicURL.Host
		}}
	connection, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	if err := s.appServerTunnels.bind(session, connection, mobile); err != nil {
		_ = connection.Close()
		return
	}
	<-session.done
}
