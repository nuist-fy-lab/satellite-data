// Package pgcompat keeps the satellite state store's positional SQL interface
// while using a native, concurrent PostgreSQL connection pool.
package pgcompat

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"regexp"
	"strings"

	"github.com/lib/pq"
)

var indexHint = regexp.MustCompile(`(?i)\s+INDEXED\s+BY\s+[a-z][a-z0-9_]*`)

func init() {
	sql.Register("satellite-postgres", markerDriver{})
}

// BindMarkers replaces placeholders outside quoted text and SQL comments.
// This is intentionally scoped to '?' used by the state store.
func BindMarkers(query string) string {
	query = indexHint.ReplaceAllString(query, "")
	var b strings.Builder
	b.Grow(len(query) + 12)
	number, state := 0, byte(0)
	for i := 0; i < len(query); i++ {
		ch := query[i]
		switch state {
		case 1, 2:
			b.WriteByte(ch)
			quote := byte('\'')
			if state == 2 {
				quote = '"'
			}
			if ch == quote {
				if i+1 < len(query) && query[i+1] == quote {
					i++
					b.WriteByte(query[i])
				} else {
					state = 0
				}
			}
		case 3:
			b.WriteByte(ch)
			if ch == '\n' {
				state = 0
			}
		case 4:
			b.WriteByte(ch)
			if ch == '*' && i+1 < len(query) && query[i+1] == '/' {
				i++
				b.WriteByte('/')
				state = 0
			}
		default:
			switch {
			case ch == '\'', ch == '"':
				if ch == '\'' {
					state = 1
				} else {
					state = 2
				}
				b.WriteByte(ch)
			case ch == '-' && i+1 < len(query) && query[i+1] == '-':
				state = 3
				b.WriteString("--")
				i++
			case ch == '/' && i+1 < len(query) && query[i+1] == '*':
				state = 4
				b.WriteString("/*")
				i++
			case ch == '?':
				number++
				fmt.Fprintf(&b, "$%d", number)
			default:
				b.WriteByte(ch)
			}
		}
	}
	return b.String()
}

type markerDriver struct{}

func (markerDriver) Open(name string) (driver.Conn, error) {
	inner, err := (&pq.Driver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return &markerConn{Conn: inner}, nil
}

type markerConn struct{ driver.Conn }

func (c *markerConn) Prepare(query string) (driver.Stmt, error) {
	return c.Conn.Prepare(BindMarkers(query))
}
func (c *markerConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if v, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return v.PrepareContext(ctx, BindMarkers(query))
	}
	return c.Prepare(query)
}
func (c *markerConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if v, ok := c.Conn.(driver.ConnBeginTx); ok {
		return v.BeginTx(ctx, opts)
	}
	if opts.Isolation != driver.IsolationLevel(sql.LevelDefault) || opts.ReadOnly {
		return nil, fmt.Errorf("unsupported transaction options")
	}
	return c.Begin()
}
func (c *markerConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if v, ok := c.Conn.(driver.ExecerContext); ok {
		return v.ExecContext(ctx, BindMarkers(query), args)
	}
	return nil, driver.ErrSkip
}
func (c *markerConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if v, ok := c.Conn.(driver.QueryerContext); ok {
		return v.QueryContext(ctx, BindMarkers(query), args)
	}
	return nil, driver.ErrSkip
}
func (c *markerConn) Ping(ctx context.Context) error {
	if v, ok := c.Conn.(driver.Pinger); ok {
		return v.Ping(ctx)
	}
	return nil
}
