package main

import "unsafe"

// Host functions (website/src/docs/developer/functions.mdx#host-functions-api), canonical module env.

//go:wasmimport env get_caller_wallet
func getCallerWallet() uint64

//go:wasmimport env get_caller_jwt_subject
func getCallerJWTSubject() uint64

//go:wasmimport env get_caller_device_id
func getCallerDeviceID() uint64

//go:wasmimport env get_request_id
func getRequestID() uint64

//go:wasmimport env get_env
func getEnv(p, n uint32) uint64

//go:wasmimport env get_secret
func getSecret(p, n uint32) uint64

//go:wasmimport env db_query_v2
func dbQueryV2(sp, sn, ap, an uint32) uint64

//go:wasmimport env db_execute_v2
func dbExecuteV2(sp, sn, ap, an uint32) uint64

//go:wasmimport env db_transaction
func dbTransaction(p, n uint32) uint64

//go:wasmimport env db_query_batch
func dbQueryBatch(p, n uint32) uint64

//go:wasmimport env cache_get
func cacheGet(p, n uint32) uint64

//go:wasmimport env cache_set
func cacheSet(kp, kn, vp, vn uint32, ttl int64)

//go:wasmimport env cache_delete
func cacheDelete(p, n uint32) uint32

//go:wasmimport env cache_incr_by
func cacheIncrBy(p, n uint32, delta int64) int64

//go:wasmimport env cache_incr_ttl
func cacheIncrTTL(p, n uint32, delta, ttl int64) int64

//go:wasmimport env http_fetch
func httpFetch(mp, mn, up, un, hp, hn, bp, bn uint32) uint64

//go:wasmimport env anon_fetch
func anonFetch(mp, mn, up, un, hp, hn, bp, bn uint32) uint64

//go:wasmimport env pubsub_publish
func pubsubPublish(tp, tn, dp, dn uint32) uint32

//go:wasmimport env push_send_v2
func pushSendV2(up, un, mp, mn uint32) uint64

//go:wasmimport env push_send_topic
func pushSendTopic(tp, tn, mp, mn uint32) uint64

//go:wasmimport env turn_credentials
func turnCredentials() uint64

//go:wasmimport env ephemeral_state_set
func ephemeralStateSet(tp, tn, kp, kn, pp, pn uint32, ttlMs int64) uint32

//go:wasmimport env ephemeral_state_clear
func ephemeralStateClear(tp, tn, kp, kn uint32) uint32

//go:wasmimport env ephemeral_state_list
func ephemeralStateList(tp, tn uint32) uint64

//go:wasmimport env function_invoke
func functionInvoke(np, nn, pp, pn uint32) uint64

//go:wasmimport env function_invoke_last_error
func functionInvokeLastError() uint64

//go:wasmimport env ws_send
func wsSend(cp, cn, dp, dn uint32) uint32

//go:wasmimport host log_info
func logInfo(p, n uint32)

//go:wasmimport orama get_ws_client_id
func getWSClientID() uint64

// ptr is the guest address of s's bytes; 0 for an empty string.
func ptr(s string) uint32 {
	if s == "" {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(unsafe.StringData(s))))
}

func size(s string) uint32 { return uint32(len(s)) }

// unpack reads the host's packed ptr<<32|len result; 0 is "nothing".
func unpack(v uint64) string {
	if v == 0 {
		return ""
	}
	p, n := uint32(v>>32), uint32(v)
	return string(unsafe.Slice((*byte)(unsafe.Pointer(uintptr(p))), n))
}
