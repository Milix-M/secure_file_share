package ws

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// Upgrader returns a websocket upgrader with security conscious defaults.
func Upgrader() websocket.Upgrader {
	return websocket.Upgrader{
		ReadBufferSize:  0,
		WriteBufferSize: 0,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
}

// Stream copies the given reader into the websocket connection as a single binary message.
func Stream(ctx context.Context, conn *websocket.Conn, reader io.Reader, idlePing time.Duration) error {
	writer, err := conn.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return err
	}

	deadlineWriter := &deadlineWriter{
		conn: conn,
		idle: idlePing,
		w:    writer,
	}

	buf := make([]byte, 64*1024)
	_, copyErr := io.CopyBuffer(deadlineWriter, ctxAwareReader{ctx: ctx, r: reader}, buf)
	closeErr := writer.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

type deadlineWriter struct {
	conn *websocket.Conn
	idle time.Duration
	w    io.Writer
}

func (dw *deadlineWriter) Write(p []byte) (int, error) {
	if err := dw.conn.SetWriteDeadline(time.Now().Add(dw.idle)); err != nil {
		return 0, err
	}
	return dw.w.Write(p)
}

type ctxAwareReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxAwareReader) Read(p []byte) (int, error) {
	select {
	case <-c.ctx.Done():
		return 0, c.ctx.Err()
	default:
	}
	return c.r.Read(p)
}
