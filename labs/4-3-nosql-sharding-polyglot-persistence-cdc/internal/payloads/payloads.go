// Package payloads is the wire-format the lab's services share.
package payloads

import "time"

// Product mirrors the Postgres `products` row plus a transient _lsn used
// by the CDC pipeline.
type Product struct {
	ID           int64     `json:"id"           bson:"id"`
	TenantID     string    `json:"tenant_id"    bson:"tenant_id"`
	SKU          string    `json:"sku"          bson:"sku"`
	Title        string    `json:"title"        bson:"title"`
	Description  string    `json:"description"  bson:"description"`
	PriceCents   int64     `json:"price_cents"  bson:"price_cents"`
	Stock        int       `json:"stock"        bson:"stock"`
	SearchFacets []string  `json:"search_facets" bson:"search_facets"`
	CommittedAt  time.Time `json:"committed_at" bson:"committed_at"`
	UpdatedAt    time.Time `json:"updated_at"   bson:"updated_at"`
	LSN          string    `json:"_lsn,omitempty" bson:"_lsn,omitempty"`
}

// Order mirrors the Postgres `orders` row.
type Order struct {
	ID          int64     `json:"id"           bson:"id"`
	TenantID    string    `json:"tenant_id"    bson:"tenant_id"`
	UserID      int64     `json:"user_id"      bson:"user_id"`
	ProductID   int64     `json:"product_id"   bson:"product_id"`
	Quantity    int       `json:"quantity"     bson:"quantity"`
	TotalCents  int64     `json:"total_cents"  bson:"total_cents"`
	Status      string    `json:"status"       bson:"status"`
	CommittedAt time.Time `json:"committed_at" bson:"committed_at"`
	UpdatedAt   time.Time `json:"updated_at"   bson:"updated_at"`
	LSN         string    `json:"_lsn,omitempty" bson:"_lsn,omitempty"`
}

// User mirrors the Postgres `users` row.
type User struct {
	ID          int64     `json:"id"           bson:"id"`
	TenantID    string    `json:"tenant_id"    bson:"tenant_id"`
	Email       string    `json:"email"        bson:"email"`
	DisplayName string    `json:"display_name" bson:"display_name"`
	CommittedAt time.Time `json:"committed_at" bson:"committed_at"`
	UpdatedAt   time.Time `json:"updated_at"   bson:"updated_at"`
	LSN         string    `json:"_lsn,omitempty" bson:"_lsn,omitempty"`
}

// MongoEvent is the document shape the loadgen writes into the four
// pre-sharded collections.
type MongoEvent struct {
	EventID         string    `bson:"event_id"`
	TenantID        string    `bson:"tenant_id"`
	TenantPartition string    `bson:"tenant_partition,omitempty"`
	UserHash        int32     `bson:"user_hash,omitempty"`
	UserID          int64     `bson:"user_id"`
	ProductID       int64     `bson:"product_id"`
	Quantity        int       `bson:"quantity"`
	TotalCents      int64     `bson:"total_cents"`
	OccurredAt      time.Time `bson:"occurred_at"`
}
