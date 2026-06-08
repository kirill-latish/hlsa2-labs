// Package mongoutil holds the small bits of Mongo wiring shared by the
// services. Connection construction, server-status helpers, and the
// "wait for cluster healthy" preamble are all here so the bench/loadgen
// binaries don't repeat themselves.
package mongoutil

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

// ConnectMongos returns a Client targeting the comma-separated list of
// mongos endpoints (the loadgen rotates between them).
func ConnectMongos(ctx context.Context, hosts string) (*mongo.Client, error) {
	uri := buildURI(hosts)
	opts := options.Client().
		ApplyURI(uri).
		SetWriteConcern(writeconcern.Majority()).
		SetReadConcern(readconcern.Local()).
		SetMaxPoolSize(64).
		SetServerSelectionTimeout(10 * time.Second)
	c, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, err
	}
	if err := c.Ping(ctx, nil); err != nil {
		return nil, err
	}
	return c, nil
}

// ConnectShard returns a Client pinned to a single shard mongod (used
// by partition-stats to scrape per-shard server-status).
func ConnectShard(ctx context.Context, host string) (*mongo.Client, error) {
	uri := fmt.Sprintf("mongodb://%s/?directConnection=true", host)
	opts := options.Client().
		ApplyURI(uri).
		SetServerSelectionTimeout(5 * time.Second)
	c, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, err
	}
	if err := c.Ping(ctx, nil); err != nil {
		return nil, err
	}
	return c, nil
}

func buildURI(hosts string) string {
	parts := strings.Split(strings.TrimSpace(hosts), ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return fmt.Sprintf("mongodb://%s/?retryWrites=true&w=majority", strings.Join(parts, ","))
}

// ServerStatus runs db.adminCommand({serverStatus:1}) and returns the
// raw bson.M so callers can pluck whatever they need.
func ServerStatus(ctx context.Context, c *mongo.Client) (bson.M, error) {
	var out bson.M
	err := c.Database("admin").RunCommand(ctx, bson.D{{Key: "serverStatus", Value: 1}}).Decode(&out)
	return out, err
}

// CollectionStats returns the {storageStats: {count, size}} for a
// given collection.
func CollectionStats(ctx context.Context, c *mongo.Client, db, coll string) (bson.M, error) {
	cursor, err := c.Database(db).Collection(coll).Aggregate(ctx, []bson.D{
		{{Key: "$collStats", Value: bson.M{"storageStats": bson.M{}}}},
	})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var out bson.M
	if cursor.Next(ctx) {
		if err := cursor.Decode(&out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ShardDistribution runs db.runCommand({collStats:..}) against the
// mongos and returns a per-shard doc-count map.
func ShardDistribution(ctx context.Context, c *mongo.Client, db, coll string) (map[string]int64, error) {
	var out bson.M
	err := c.Database(db).RunCommand(ctx, bson.D{{Key: "collStats", Value: coll}}).Decode(&out)
	if err != nil {
		return nil, err
	}
	res := map[string]int64{}
	if shards, ok := out["shards"].(bson.M); ok {
		for shardName, raw := range shards {
			if shardDoc, ok := raw.(bson.M); ok {
				if cnt, ok := toInt64(shardDoc["count"]); ok {
					res[shardName] = cnt
				}
			}
		}
	}
	return res, nil
}

func toInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case int32:
		return int64(t), true
	case int64:
		return t, true
	case float64:
		return int64(t), true
	}
	return 0, false
}
