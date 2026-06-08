// Initialise the config-server replica set.
// Run from inside one of the mongo-config-* containers via mongosh.
// Idempotent: rs.initiate() returns {ok:0, code:23} ("already initialized")
// if the RS already exists, which we swallow.

(function () {
    var cfg = {
        _id: "cfgrs",
        configsvr: true,
        members: [
            { _id: 0, host: "mongo-config-1:27017" },
            { _id: 1, host: "mongo-config-2:27017" },
            { _id: 2, host: "mongo-config-3:27017" },
        ],
    };
    try {
        var status = rs.status();
        print("[lab43] config rs already initialised, current state=" + status.myState);
    } catch (e) {
        if (e.codeName === "NotYetInitialized" || /no replset config has been received/i.test(String(e))) {
            print("[lab43] initiating config rs");
            var res = rs.initiate(cfg);
            print("[lab43] rs.initiate -> " + JSON.stringify(res));
        } else {
            print("[lab43] unexpected error checking rs.status(): " + e);
            throw e;
        }
    }
})();
