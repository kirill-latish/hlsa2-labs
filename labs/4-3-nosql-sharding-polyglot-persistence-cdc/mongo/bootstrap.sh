#!/usr/bin/env bash
# Run by the mongo-bootstrap one-shot container. Initialises both replica
# sets and registers the four pre-sharded collections.
set -euo pipefail

echo "[lab43] waiting for mongo-config-1 to accept connections..."
for i in $(seq 1 60); do
    if mongosh --quiet --host mongo-config-1:27017 --eval 'db.runCommand({ping:1}).ok' >/dev/null 2>&1; then
        break
    fi
    sleep 2
done

echo "[lab43] initialising config replica set"
mongosh --quiet --host mongo-config-1:27017 /init/config-init.js

echo "[lab43] waiting for cfgrs primary..."
for i in $(seq 1 60); do
    state=$(mongosh --quiet --host mongo-config-1:27017 --eval 'try{rs.status().myState}catch(e){-1}' 2>/dev/null || echo "-1")
    if [[ "${state}" == "1" ]]; then
        break
    fi
    sleep 2
done

# We need a mongos-aware shell to add shards + enable sharding. mongos isn't
# up yet (it depends_on this container), so we initialise the shard replica
# sets directly via mongosh against each shard, and defer the
# `addShard / shardCollection` calls to the first `seed.sh` invocation.
echo "[lab43] initialising shard replica sets"
for host in mongo-shard-1:27017 mongo-shard-2:27017 mongo-shard-3:27017; do
    rs=$(case "${host}" in
        mongo-shard-1:*) echo shard1rs ;;
        mongo-shard-2:*) echo shard2rs ;;
        mongo-shard-3:*) echo shard3rs ;;
    esac)
    echo "[lab43] -> ${host} (${rs})"
    mongosh --quiet --host "${host}" --eval "
        try {
            var st = rs.status();
            print('[lab43] ${host} already initialised, state=' + st.myState);
        } catch (e) {
            var res = rs.initiate({_id:'${rs}', members:[{_id:0, host:'${host}'}]});
            print('[lab43] rs.initiate -> ' + JSON.stringify(res));
        }
    "
done

echo "[lab43] bootstrap complete (shards-init.js will be applied through mongos by scripts/seed.sh)"
