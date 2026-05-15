package dnspod

import (
	"context"
	"net"
)

type Config struct {
	SecretID   string
	SecretKey  string
	Domain     string
	RecordLine string
	TTL        uint64
}

type Record struct {
	ID        uint64
	Name      string
	Type      string
	Value     string
	Line      string
	TTL       uint64
	Status    string
	Managed   bool
	UpdatedOn string
}

type Client interface {
	ListRecords(ctx context.Context) ([]Record, error)
	CreateRecord(ctx context.Context, record Record) (uint64, error)
	ModifyRecord(ctx context.Context, record Record) error
	DeleteRecord(ctx context.Context, recordID uint64) error
}

func TypeForIP(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "A"
	}
	if parsed.To4() != nil {
		return "A"
	}
	return "AAAA"
}
