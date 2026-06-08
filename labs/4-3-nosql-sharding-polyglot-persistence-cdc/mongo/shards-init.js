// Bootstraps each shard mongod into its own single-node replica set, then
// adds them to the cluster via mongos and pre-shards the four collections
// used by the benches. Idempotent — every operation either succeeds the
// first time or no-ops on subsequent runs.

function initShardRS(host, replSet) {
    var conn = new Mongo(host);
    var admin = conn.getDB("admin");
    try {
        var status = admin.runCommand({ replSetGetStatus: 1 });
        if (status.ok === 1) {
            print("[lab43] shard " + host + " already in rs (" + status.myState + ")");
            return;
        }
    } catch (e) {
        // NotYetInitialized — initiate below.
    }
    print("[lab43] initiating shard " + host + " into " + replSet);
    var initRes = admin.runCommand({
        replSetInitiate: {
            _id: replSet,
            members: [{ _id: 0, host: host }],
        },
    });
    print("[lab43] replSetInitiate(" + replSet + ") -> " + JSON.stringify(initRes));
}

initShardRS("mongo-shard-1:27017", "shard1rs");
initShardRS("mongo-shard-2:27017", "shard2rs");
initShardRS("mongo-shard-3:27017", "shard3rs");

// At this point we expect to be connected to a mongos.
function addShardIfMissing(label, conn) {
    try {
        var res = sh.addShard(conn);
        print("[lab43] sh.addShard(" + conn + ") -> " + JSON.stringify(res));
    } catch (e) {
        if (/already exists/i.test(String(e))) {
            print("[lab43] shard " + label + " already added");
        } else {
            throw e;
        }
    }
}

addShardIfMissing("shard1", "shard1rs/mongo-shard-1:27017");
addShardIfMissing("shard2", "shard2rs/mongo-shard-2:27017");
addShardIfMissing("shard3", "shard3rs/mongo-shard-3:27017");

// Enable sharding on the lab database.
try {
    sh.enableSharding("lab43");
    print("[lab43] enabled sharding on lab43");
} catch (e) {
    print("[lab43] enableSharding('lab43'): " + e);
}

// Lab knob: shrink chunk size so the balancer auto-migrates chunks
// under realistic lab loads (~MB/run) rather than waiting for the
// default 128MB threshold.
try {
    db.getSiblingDB("config").settings.updateOne(
        { _id: "chunksize" },
        { $set: { _id: "chunksize", value: 1 } },
        { upsert: true }
    );
    print("[lab43] config.settings.chunksize = 1MB");
} catch (e) {
    print("[lab43] could not set chunksize: " + e);
}

var lab = db.getSiblingDB("lab43");

function ensureCollection(name) {
    var existing = lab.getCollectionNames();
    if (existing.indexOf(name) === -1) {
        lab.createCollection(name);
        print("[lab43] created lab43." + name);
    }
}

["events_candidate", "events_hash_suffix", "events_composite", "events_resharded"].forEach(ensureCollection);

function shardCollectionIfNeeded(coll, key) {
    var ns = "lab43." + coll;
    var meta = db.getSiblingDB("config").collections.findOne({ _id: ns });
    if (meta && meta.key) {
        print("[lab43] " + ns + " already sharded with key=" + JSON.stringify(meta.key));
        return;
    }
    var res = sh.shardCollection(ns, key);
    print("[lab43] shardCollection(" + ns + ", " + JSON.stringify(key) + ") -> " + JSON.stringify(res));
}

// candidate: tenant_id only - the deliberately-bad starting point.
shardCollectionIfNeeded("events_candidate",   { tenant_id: 1 });
// hash-suffix: tenant_id + bucket; loadgen splits a hot tenant into N buckets.
shardCollectionIfNeeded("events_hash_suffix", { tenant_partition: 1 });
// composite: balances within a tenant by user_hash.
shardCollectionIfNeeded("events_composite",   { tenant_id: 1, user_hash: 1 });
// resharded: pure hashed shard key on user_hash.
shardCollectionIfNeeded("events_resharded",   { user_hash: "hashed" });

// Pre-split the keyspaces so the balancer doesn't have to migrate the
// initial monolithic chunk. We pre-create boundaries that map cleanly
// onto the three shards.
function safeSplitAt(ns, key) {
    try {
        sh.splitAt(ns, key);
        print("[lab43] split " + ns + " at " + JSON.stringify(key));
    } catch (e) {
        if (/already exists/i.test(String(e)) || /below the median/i.test(String(e))) {
            print("[lab43] split " + ns + " " + JSON.stringify(key) + " no-op: " + e);
        } else {
            print("[lab43] split " + ns + " " + JSON.stringify(key) + ": " + e);
        }
    }
}

// candidate: split the tenant_id range into thirds.
safeSplitAt("lab43.events_candidate", { tenant_id: "tenant-22" });
safeSplitAt("lab43.events_candidate", { tenant_id: "tenant-44" });

// hash-suffix: tenant_partition is "tenant-N:NN" so splits can be by prefix.
safeSplitAt("lab43.events_hash_suffix", { tenant_partition: "tenant-22:00" });
safeSplitAt("lab43.events_hash_suffix", { tenant_partition: "tenant-44:00" });

// composite: split on the tenant_id dimension first (so different
// tenants land on different shards), then split the *expected hot*
// tenant on user_hash so a celebrity workload doesn't pile onto one
// shard. The "tenant-A" split is what makes the composite key beat the
// candidate key under hot-entity load.
safeSplitAt("lab43.events_composite", { tenant_id: "tenant-22", user_hash: MinKey });
safeSplitAt("lab43.events_composite", { tenant_id: "tenant-44", user_hash: MinKey });
safeSplitAt("lab43.events_composite", { tenant_id: "tenant-A",  user_hash: 341 });
safeSplitAt("lab43.events_composite", { tenant_id: "tenant-A",  user_hash: 682 });

// resharded: hashed shard key auto-creates two chunks at sh.shardCollection
// time when numInitialChunks isn't specified. We don't pre-split.

// Move the second/third chunks to the other shards so the very first
// write is balanced. Idempotent — already-on-target moveChunk is a noop.
function safeMove(ns, find, to) {
    try {
        sh.moveChunk(ns, find, to);
        print("[lab43] moveChunk " + ns + " " + JSON.stringify(find) + " -> " + to);
    } catch (e) {
        print("[lab43] moveChunk " + ns + " " + JSON.stringify(find) + " -> " + to + ": " + e);
    }
}

safeMove("lab43.events_candidate", { tenant_id: "tenant-22" }, "shard2rs");
safeMove("lab43.events_candidate", { tenant_id: "tenant-44" }, "shard3rs");
safeMove("lab43.events_hash_suffix", { tenant_partition: "tenant-22:00" }, "shard2rs");
safeMove("lab43.events_hash_suffix", { tenant_partition: "tenant-44:00" }, "shard3rs");
safeMove("lab43.events_composite", { tenant_id: "tenant-22", user_hash: MinKey }, "shard2rs");
safeMove("lab43.events_composite", { tenant_id: "tenant-44", user_hash: MinKey }, "shard3rs");
safeMove("lab43.events_composite", { tenant_id: "tenant-A",  user_hash: 341 }, "shard2rs");
safeMove("lab43.events_composite", { tenant_id: "tenant-A",  user_hash: 682 }, "shard3rs");

print("[lab43] shards-init complete");
