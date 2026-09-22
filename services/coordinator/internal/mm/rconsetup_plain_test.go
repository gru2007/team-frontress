// This file intentionally left as an empty stub.
//
// It used to test the fake-RCON match-admission classifier
// (classifyMatchAddReply/hasExactReply/etc.), which was removed when
// server roster delivery moved to the native GC protocol -- see
// internal/mm/gcpusher.go and internal/gcparty's PushMatchRoster.
//
// The file is kept in place (rather than deleted) because the sandbox
// used to push it here from the coordinator sandbox cannot delete Mac
// files, only write them.
package mm
