// Command function is the serverless feature's fixture: TinyGo, WASI. It
// dispatches on the input's "op" and reports what the host functions
// answered, so the tests assert on the gateway's behaviour and not on the
// fixture's. With env E2E_MODE=cron or pubsub it records each trigger fire
// in its own table instead.
package main

import (
	"encoding/json"
	"io"
	"os"
	"strconv"
	"time"
)

type request struct {
	Op      string            `json:"op"`
	Name    string            `json:"name"`
	Key     string            `json:"key"`
	Value   string            `json:"value"`
	Topic   string            `json:"topic"`
	SQL     string            `json:"sql"`
	Args    []any             `json:"args"`
	Ops     json.RawMessage   `json:"ops"`
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Payload string            `json:"payload"`
	Size    int               `json:"size"`
	Count   int               `json:"count"`
	TTL     int64             `json:"ttl"`
	Delta   int64             `json:"delta"`
	Millis  int64             `json:"ms"`
	Data    json.RawMessage   `json:"data"`
	Depth   int               `json:"trigger_depth"`
}

func main() {
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		reply(map[string]any{"error": "read input: " + err.Error()})
		return
	}
	var req request
	if len(in) > 0 {
		if err := json.Unmarshal(in, &req); err != nil {
			reply(map[string]any{"error": "input is not JSON: " + err.Error()})
			return
		}
	}
	switch unpack(getEnv(ptr("E2E_MODE"), size("E2E_MODE"))) {
	case "cron":
		record("cron", req)
		return
	case "pubsub":
		record("pubsub", req)
		if req.Topic != "" {
			pubsubPublish(ptr(req.Topic), size(req.Topic), ptr(`{"again":true}`), size(`{"again":true}`))
		}
		return
	}
	reply(dispatch(req))
}

// record inserts one row per fire into e2e_fires. The slot is the UTC
// two-minute bucket, the period of the tests' cron schedule, so two fires of
// one scheduled run are visible as a count > 1.
func record(kind string, req request) {
	const ddl = `CREATE TABLE IF NOT EXISTS e2e_fires (id INTEGER PRIMARY KEY, kind TEXT, slot TEXT, depth INTEGER, request_id TEXT)`
	dbExecuteV2(ptr(ddl), size(ddl), 0, 0)
	const ins = `INSERT INTO e2e_fires (kind, slot, depth, request_id) VALUES (?, strftime('%Y-%m-%d %H:','now') || (CAST(strftime('%M','now') AS INTEGER) / 2), ?, ?)`
	args, _ := json.Marshal([]any{kind, req.Depth, unpack(getRequestID())})
	a := string(args)
	res := unpack(dbExecuteV2(ptr(ins), size(ins), ptr(a), size(a)))
	os.Stdout.Write([]byte(res))
}

func reply(v any) {
	out, err := json.Marshal(v)
	if err != nil {
		out = []byte(`{"error":"encode reply"}`)
	}
	os.Stdout.Write(out)
}

func raw(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage("null")
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	b, _ := json.Marshal(s)
	return b
}

func dispatch(req request) any {
	switch req.Op {
	case "whoami":
		// With value "send" the answer also goes to the socket, as a
		// function_invoke_async target's must.
		out := map[string]any{
			"wallet": unpack(getCallerWallet()), "subject": unpack(getCallerJWTSubject()),
			"device": unpack(getCallerDeviceID()), "request_id": unpack(getRequestID()),
			"env": unpack(getEnv(ptr(req.Key), size(req.Key))), "ws_client": unpack(getWSClientID()),
			"verdict": raw(unpack(getCallerAttestationVerdict())), "id": req.Key,
		}
		if req.Value == "send" {
			b, _ := json.Marshal(out)
			msg, client := string(b), unpack(getWSClientID())
			wsSend(ptr(client), size(client), ptr(msg), size(msg))
		}
		return out
	case "secret":
		return map[string]any{"value": unpack(getSecret(ptr(req.Name), size(req.Name)))}
	case "echo":
		logInfo(ptr("e2e-log:"+req.Value), size("e2e-log:"+req.Value))
		return map[string]any{"echo": req.Value}
	case "spin":
		deadline := time.Now().Add(time.Duration(req.Millis) * time.Millisecond)
		n := 0
		for time.Now().Before(deadline) {
			n++
		}
		return map[string]any{"spun": n}
	case "reads":
		// Count sequential reads: the shape of an op a persistent socket's
		// router runs. With value "send" the op answers the socket itself, as
		// a function_invoke_async target must.
		const one = "SELECT 1"
		for i := 0; i < req.Count; i++ {
			dbQueryV2(ptr(one), size(one), 0, 0)
		}
		out := map[string]any{"id": req.Key, "reads": req.Count}
		if req.Value == "send" {
			b, _ := json.Marshal(out)
			msg, client := string(b), unpack(getWSClientID())
			wsSend(ptr(client), size(client), ptr(msg), size(msg))
		}
		return out
	case "alloc":
		b := make([]byte, req.Size<<20)
		for i := range b {
			b[i] = byte(i)
		}
		return map[string]any{"allocated_mb": req.Size, "last": b[len(b)-1]}
	}
	return dispatchData(req)
}

func dispatchData(req request) any {
	args, _ := json.Marshal(req.Args)
	a := string(args)
	ops := string(req.Ops)
	switch req.Op {
	case "db_exec":
		return raw(unpack(dbExecuteV2(ptr(req.SQL), size(req.SQL), ptr(a), size(a))))
	case "db_query":
		return raw(unpack(dbQueryV2(ptr(req.SQL), size(req.SQL), ptr(a), size(a))))
	case "db_tx":
		return raw(unpack(dbTransaction(ptr(ops), size(ops))))
	case "db_batch":
		return raw(unpack(dbQueryBatch(ptr(ops), size(ops))))
	case "db_batch_sum":
		return summarize(unpack(dbQueryBatch(ptr(ops), size(ops))))
	case "cache_set":
		for i := 0; i < max(req.Count, 1); i++ {
			cacheSet(ptr(req.Key), size(req.Key), ptr(req.Value), size(req.Value), req.TTL)
		}
		return map[string]any{"set": true}
	case "cache_get":
		return map[string]any{"value": unpack(cacheGet(ptr(req.Key), size(req.Key)))}
	case "cache_delete":
		return map[string]any{"deleted": cacheDelete(ptr(req.Key), size(req.Key))}
	case "cache_incr":
		return map[string]any{"value": cacheIncrBy(ptr(req.Key), size(req.Key), req.Delta)}
	case "cache_incr_ttl":
		return map[string]any{"value": cacheIncrTTL(ptr(req.Key), size(req.Key), req.Delta, req.TTL)}
	}
	return dispatchNet(req)
}

func dispatchNet(req request) any {
	switch req.Op {
	case "fetch", "anon_fetch":
		h, _ := json.Marshal(req.Headers)
		hs := string(h)
		m := req.Method
		if m == "" {
			m = "GET"
		}
		var out uint64
		if req.Op == "anon_fetch" {
			out = anonFetch(ptr(m), size(m), ptr(req.URL), size(req.URL), ptr(hs), size(hs), 0, 0)
		} else {
			out = httpFetch(ptr(m), size(m), ptr(req.URL), size(req.URL), ptr(hs), size(hs), 0, 0)
		}
		return map[string]any{"result": raw(unpack(out))}
	case "publish":
		d := string(req.Data)
		return map[string]any{"published": pubsubPublish(ptr(req.Topic), size(req.Topic), ptr(d), size(d))}
	case "push_user":
		d := string(req.Data)
		return map[string]any{"result": raw(unpack(pushSendV2(ptr(req.Key), size(req.Key), ptr(d), size(d))))}
	case "push_topic":
		d := string(req.Data)
		return map[string]any{"result": raw(unpack(pushSendTopic(ptr(req.Key), size(req.Key), ptr(d), size(d))))}
	case "turn":
		return map[string]any{"result": raw(unpack(turnCredentials()))}
	case "nested":
		p := string(req.Data)
		out := unpack(functionInvoke(ptr(req.Name), size(req.Name), ptr(p), size(p)))
		return map[string]any{"result": raw(out), "last_error": raw(unpack(functionInvokeLastError()))}
	}
	return dispatchEphemeral(req)
}

func dispatchEphemeral(req request) any {
	switch req.Op {
	case "eph_set":
		payload := req.Payload
		for len(payload) < req.Size {
			payload += "x"
		}
		ok := 0
		for i := 0; i < max(req.Count, 1); i++ {
			key := req.Key
			if req.Count > 0 {
				key = req.Key + "-" + strconv.Itoa(i)
			}
			ok += int(ephemeralStateSet(ptr(req.Topic), size(req.Topic), ptr(key), size(key), ptr(payload), size(payload), req.TTL))
		}
		return map[string]any{"ok": ok}
	case "eph_clear":
		return map[string]any{"ok": ephemeralStateClear(ptr(req.Topic), size(req.Topic), ptr(req.Key), size(req.Key))}
	case "eph_list":
		return map[string]any{"result": raw(unpack(ephemeralStateList(ptr(req.Topic), size(req.Topic))))}
	}
	return map[string]any{"error": "unknown op " + strconv.Quote(req.Op)}
}

// summarize reduces a batch result to its per-op row counts and codes, for
// batches whose rows are too large to return.
func summarize(result string) any {
	var r struct {
		Results []struct {
			Rows []json.RawMessage `json:"rows"`
			Code string            `json:"code"`
		} `json:"results"`
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal([]byte(result), &r); err != nil {
		return map[string]any{"error": "batch result is not JSON: " + err.Error(), "bytes": len(result)}
	}
	ops := make([]map[string]any, 0, len(r.Results))
	for _, op := range r.Results {
		ops = append(ops, map[string]any{"rows": len(op.Rows), "code": op.Code})
	}
	return map[string]any{"ops": ops, "error": r.Error, "code": r.Code}
}
