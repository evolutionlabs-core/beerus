package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"syscall"

	"github.com/evolutionlabs-core/beerus/internal/nip"
)

const (
	NIBSSHost          = "127.0.0.1"
	NIBSSDirectoryPath = "/v1/control/banks"
	NIBSSPage          = 500
)

type NIBSSControl struct {
	Link Link
	Base string
	Port int
}

type NIBSSDownError struct {
	Port int
	Err  error
}

func (e *NIBSSDownError) Error() string {
	return fmt.Sprintf("NIBSS is not answering on port %d: %v", e.Port, e.Err)
}

func (e *NIBSSDownError) Unwrap() error {
	return e.Err
}

func NewNIBSSControl(link Link, port int) NIBSSControl {
	return NIBSSControl{Link: link, Base: "http://" + net.JoinHostPort(NIBSSHost, strconv.Itoa(port)), Port: port}
}

func Unreachable(err error) bool {
	var op *net.OpError
	var timeout net.Error
	return errors.Is(err, syscall.ECONNREFUSED) || (errors.As(err, &op) && op.Op == "dial") || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout())
}

func PageQuery(cursor string) string {
	q := url.Values{"limit": {strconv.Itoa(NIBSSPage)}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	return q.Encode()
}

func (c NIBSSControl) Call(ctx context.Context, method, path string, header http.Header, in, out any) error {
	_, err := c.Link.Request(ctx, c.Base, method, path, header, in, out)
	var peer *PeerError
	if err == nil || errors.As(err, &peer) || ctx.Err() != nil {
		return err
	}
	if Unreachable(err) {
		return &NIBSSDownError{Port: c.Port, Err: err}
	}
	return fmt.Errorf("call NIBSS on port %d: %w", c.Port, err)
}

func (c NIBSSControl) Directory(ctx context.Context) ([]nip.Institution, error) {
	return AllPages(ctx, func(ctx context.Context, cursor string) ([]nip.Institution, string, error) {
		var page struct {
			Data       []nip.Institution `json:"data"`
			NextCursor string            `json:"next_cursor"`
		}
		err := c.Call(ctx, http.MethodGet, NIBSSDirectoryPath+"?"+PageQuery(cursor), nil, nil, &page)
		return page.Data, page.NextCursor, err
	})
}
