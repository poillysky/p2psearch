package goed2k

import (
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/goed2k/core/protocol"
	kadproto "github.com/goed2k/core/protocol/kad"
	serverproto "github.com/goed2k/core/protocol/server"
)

type SearchScope uint8

const (
	SearchScopeServer SearchScope = 1 << iota
	SearchScopeDHT
	SearchScopeGlobal // UDP GlobSearch to many servers (eMule "global search")
	SearchScopeAll    = SearchScopeServer | SearchScopeDHT | SearchScopeGlobal
)

type SearchState string

const (
	SearchStateIdle     SearchState = "IDLE"
	SearchStateRunning  SearchState = "RUNNING"
	SearchStateFinished SearchState = "FINISHED"
	SearchStateStopped  SearchState = "STOPPED"
	SearchStateFailed   SearchState = "FAILED"
)

type SearchParams struct {
	Query              string
	// Charset controls how the query is encoded on the wire and for KAD keyword
	// hashing: ""/"auto" (CJK→GBK), "gbk", or "utf8". Chinese eMule servers
	// historically index GBK; dual-pass searches use both.
	Charset            string
	Scope              SearchScope
	MinSize            int64
	MaxSize            int64
	MinSources         int
	MinCompleteSources int
	FileType           string
	Extension          string
	// GlobalServers are host:tcpPort targets for UDP GlobSearch (UDP = TCP+4).
	// Prefer servers not already covered by the TCP local search fan-out.
	GlobalServers []string
	// GlobalMax caps how many UDP targets are contacted (default 12).
	GlobalMax int
}

type SearchResultSource uint8

const (
	SearchResultServer SearchResultSource = 1 << iota
	SearchResultKAD
)

type SearchResult struct {
	Hash            protocol.Hash
	FileName        string
	FileSize        int64
	Sources         int
	CompleteSources int
	MediaBitrate    int
	MediaLength     int
	MediaCodec      string
	Extension       string
	FileType        string
	Note            string
	Source          SearchResultSource
}

func (r SearchResult) ED2KLink() string {
	if r.FileName == "" || r.FileSize <= 0 || r.Hash == protocol.Invalid {
		return ""
	}
	return FormatLink(r.FileName, r.FileSize, r.Hash)
}

// ResultKey identifies a hit for dedup. The file hash is the identity; the
// name+size pair is only a fallback for the rare entry that arrived without a
// usable hash. Note that emptiness is not a usable test here — Hash.String()
// renders the zero hash as a string of zeros — so IsZero is what decides.
func ResultKey(r SearchResult) string {
	if !r.Hash.IsZero() {
		return r.Hash.String()
	}
	return r.FileName + "|" + strconv.FormatInt(r.FileSize, 10)
}

type SearchSnapshot struct {
	ID         uint32
	Params     SearchParams
	State      SearchState
	Results    []SearchResult
	UpdatedAt  int64
	StartedAt  int64
	ServerBusy bool
	GlobalBusy bool
	DHTBusy    bool
	KadKeyword string
	Error      string
}

type SearchHandle struct {
	session *Session
	id      uint32
}

func (h SearchHandle) ID() uint32 {
	return h.id
}

func (h SearchHandle) IsValid() bool {
	return h.session != nil && h.id != 0
}

func (h SearchHandle) Snapshot() SearchSnapshot {
	if h.session == nil {
		return SearchSnapshot{}
	}
	return h.session.SearchSnapshot()
}

func (h SearchHandle) Stop() error {
	if h.session == nil {
		return nil
	}
	return h.session.StopSearch(h.id)
}

const (
	// maxKadKeywords caps how many query tokens get fanned out to KAD.
	maxKadKeywords = 3
)

type searchTask struct {
	mu         sync.Mutex
	id         uint32
	params     SearchParams
	state      SearchState
	results    map[string]SearchResult
	startedAt  int64
	updatedAt  int64
	deadlineAt int64
	serverBusy bool
	globalBusy bool
	dhtBusy    bool
	// Server phase completion tracking. One search request fans out to every
	// connected server and each answers with one or more batches; the phase ends
	// as soon as they have all reported their final batch instead of being held
	// open for a fixed timeout.
	serversSent  int
	serversDone  int
	lastServerAt int64
	// pagingServers tracks connections that returned MoreResults=true and have
	// not yet sent a terminal batch. Quiet-gap must not fire while this is non-empty.
	pagingServers map[string]struct{}
	// Global UDP phase: fire-and-collect until the hard ceiling; servers only
	// reply when they have hits, so we cannot count "done" replies.
	globalSent       int
	globalDeadlineAt int64
	lastGlobalAt     int64
	// DHT phase completion tracking: one traversal per query token.
	dhtPending int
	kadKeyword string
	errText    string
}

func newSearchTask(id uint32, params SearchParams, startedAt int64) *searchTask {
	return &searchTask{
		id:        id,
		params:    params,
		state:     SearchStateRunning,
		results:   make(map[string]SearchResult),
		startedAt: startedAt,
		updatedAt: startedAt,
	}
}

func (s *searchTask) snapshot() SearchSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	results := make([]SearchResult, 0, len(s.results))
	for _, result := range s.results {
		results = append(results, result)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Sources != results[j].Sources {
			return results[i].Sources > results[j].Sources
		}
		if results[i].CompleteSources != results[j].CompleteSources {
			return results[i].CompleteSources > results[j].CompleteSources
		}
		if results[i].FileSize != results[j].FileSize {
			return results[i].FileSize > results[j].FileSize
		}
		return results[i].FileName < results[j].FileName
	})
	return SearchSnapshot{
		ID:         s.id,
		Params:     s.params,
		State:      s.state,
		Results:    results,
		UpdatedAt:  s.updatedAt,
		StartedAt:  s.startedAt,
		ServerBusy: s.serverBusy,
		GlobalBusy: s.globalBusy,
		DHTBusy:    s.dhtBusy,
		KadKeyword: s.kadKeyword,
		Error:      s.errText,
	}
}

// serverPhaseStarted records that a search request went out to `sent` servers.
// hardTimeoutSec is only a safety ceiling for the whole server phase; normal
// completion is driven by the servers themselves (see noteServerBatch).
func (s *searchTask) serverPhaseStarted(sent int, hardTimeoutSec int) {
	s.mu.Lock()
	s.serversSent += sent
	s.serversDone = 0
	if sent > 0 {
		s.serverBusy = true
	}
	now := CurrentTime()
	s.lastServerAt = now
	s.updatedAt = now
	if hard := now + Seconds(int64(hardTimeoutSec)); hard > s.deadlineAt {
		s.deadlineAt = hard
	}
	s.mu.Unlock()
}

// noteServerBatch records one server response. more=true when the server
// announced another batch, which keeps the phase (and its ceiling) alive;
// more=false is a terminal batch and retires that server.
// serverID distinguishes connections so SearchMore pagination is not confused
// with peers that never answered.
func (s *searchTask) noteServerBatch(serverID string, more bool, hardTimeoutSec int) {
	s.mu.Lock()
	now := CurrentTime()
	s.lastServerAt = now
	s.updatedAt = now
	if serverID == "" {
		serverID = "_"
	}
	if more {
		if s.pagingServers == nil {
			s.pagingServers = make(map[string]struct{})
		}
		s.pagingServers[serverID] = struct{}{}
		if hard := now + Seconds(int64(hardTimeoutSec)); hard > s.deadlineAt {
			s.deadlineAt = hard
		}
	} else {
		delete(s.pagingServers, serverID)
		s.serversDone++
		if s.serversDone >= s.serversSent {
			s.serverBusy = false
			s.finishLocked()
		}
	}
	s.mu.Unlock()
}

// dhtPhaseStarted arms the DHT phase for n concurrent keyword traversals.
func (s *searchTask) dhtPhaseStarted(n int) {
	s.mu.Lock()
	s.dhtPending = n
	s.dhtBusy = n > 0
	s.updatedAt = CurrentTime()
	s.mu.Unlock()
}

// dhtTraversalDone retires one keyword traversal and finishes the phase when
// the last one reports in.
func (s *searchTask) dhtTraversalDone() {
	s.mu.Lock()
	if s.dhtPending > 0 {
		s.dhtPending--
	}
	if s.dhtPending == 0 {
		s.dhtBusy = false
		s.finishLocked()
	}
	s.updatedAt = CurrentTime()
	s.mu.Unlock()
}

// noteKadKeyword appends a token to the diagnostic keyword list.
func (s *searchTask) noteKadKeyword(keyword string) {
	if keyword == "" {
		return
	}
	s.mu.Lock()
	if s.kadKeyword == "" {
		s.kadKeyword = keyword
	} else {
		s.kadKeyword += "," + keyword
	}
	s.mu.Unlock()
}

func (s *searchTask) stop() {
	s.mu.Lock()
	s.serverBusy = false
	s.globalBusy = false
	s.dhtBusy = false
	s.dhtPending = 0
	s.state = SearchStateStopped
	s.updatedAt = CurrentTime()
	s.mu.Unlock()
}

func (s *searchTask) fail(err error) {
	s.mu.Lock()
	s.serverBusy = false
	s.globalBusy = false
	s.dhtBusy = false
	s.dhtPending = 0
	s.state = SearchStateFailed
	if err != nil {
		s.errText = err.Error()
	}
	s.updatedAt = CurrentTime()
	s.mu.Unlock()
}

func (s *searchTask) onTick(now int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != SearchStateRunning {
		return
	}
	if s.serverBusy {
		// End the server phase when every contacted server has reported a
		// terminal batch, the hard safety ceiling fires, or silent peers
		// timed out — but never while SearchMore pagination is in flight.
		if s.serversSent > 0 && s.serversDone >= s.serversSent {
			s.serverBusy = false
		} else if s.deadlineAt > 0 && now >= s.deadlineAt {
			s.serverBusy = false
		} else if len(s.pagingServers) == 0 && s.serversDone > 0 &&
			s.lastServerAt > 0 && now-s.lastServerAt >= Seconds(4) {
			s.serverBusy = false
		}
	}
	if s.globalBusy {
		if s.globalDeadlineAt > 0 && now >= s.globalDeadlineAt {
			s.globalBusy = false
		}
	}
	s.finishLocked()
}

func (s *searchTask) finishLocked() {
	if s.state != SearchStateRunning {
		return
	}
	if !s.serverBusy && !s.globalBusy && !s.dhtBusy {
		s.state = SearchStateFinished
	}
	s.updatedAt = CurrentTime()
}

// globalPhaseStarted arms the UDP GlobSearch collect window.
func (s *searchTask) globalPhaseStarted(sent, hardTimeoutSec int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.globalSent = sent
	if sent > 0 {
		s.globalBusy = true
		now := CurrentTime()
		s.lastGlobalAt = now
		s.updatedAt = now
		if hardTimeoutSec <= 0 {
			hardTimeoutSec = 12
		}
		s.globalDeadlineAt = now + Seconds(int64(hardTimeoutSec))
		if s.globalDeadlineAt > s.deadlineAt {
			s.deadlineAt = s.globalDeadlineAt
		}
	}
}

// noteGlobalHit refreshes the global phase activity timestamp.
func (s *searchTask) noteGlobalHit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := CurrentTime()
	s.lastGlobalAt = now
	s.updatedAt = now
}

func (s *searchTask) mergeResult(result SearchResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := ResultKey(result)
	if existing, ok := s.results[key]; ok {
		if existing.FileName == "" {
			existing.FileName = result.FileName
		}
		if existing.FileSize == 0 {
			existing.FileSize = result.FileSize
		}
		if result.Sources > existing.Sources {
			existing.Sources = result.Sources
		}
		if result.CompleteSources > existing.CompleteSources {
			existing.CompleteSources = result.CompleteSources
		}
		if existing.MediaBitrate == 0 {
			existing.MediaBitrate = result.MediaBitrate
		}
		if existing.MediaLength == 0 {
			existing.MediaLength = result.MediaLength
		}
		if existing.MediaCodec == "" {
			existing.MediaCodec = result.MediaCodec
		}
		if existing.Extension == "" {
			existing.Extension = result.Extension
		}
		if existing.FileType == "" {
			existing.FileType = result.FileType
		}
		if existing.Note == "" {
			existing.Note = result.Note
		}
		existing.Source |= result.Source
		s.results[key] = existing
	} else {
		s.results[key] = result
	}
	s.updatedAt = CurrentTime()
}

func makeSearchResultFromServer(entry serverproto.SharedFileEntry) SearchResult {
	result := SearchResult{
		Hash:   entry.Hash,
		Source: SearchResultServer,
	}
	if name, ok := entry.StringTag(protocol.FTFilename); ok {
		result.FileName = name
	}
	if size, ok := entry.UIntTag(protocol.FTFileSize); ok {
		result.FileSize = int64(size)
	}
	if hi, ok := entry.UIntTag(protocol.FTFileSizeHi); ok {
		result.FileSize += int64(hi << 32)
	}
	if sources, ok := entry.UIntTag(protocol.FTSources); ok {
		result.Sources = int(sources)
	}
	if complete, ok := entry.UIntTag(protocol.FTCompleteSources); ok {
		result.CompleteSources = int(complete)
	}
	if bitrate, ok := entry.UIntTag(protocol.FTMediaBitrate); ok {
		result.MediaBitrate = int(bitrate)
	}
	if length, ok := entry.UIntTag(protocol.FTMediaLength); ok {
		result.MediaLength = int(length)
	}
	if codec, ok := entry.StringTag(protocol.FTMediaCodec); ok {
		result.MediaCodec = codec
	}
	if ext, ok := entry.StringTag(protocol.FTFileFormat); ok {
		result.Extension = ext
	}
	if fileType, ok := entry.StringTag(protocol.FTFileType); ok {
		result.FileType = fileType
	}
	return result
}

func makeSearchResultFromKAD(entry kadproto.SearchEntry) SearchResult {
	result := SearchResult{
		Hash:   entry.ID.Hash,
		Source: SearchResultKAD,
	}
	if name, ok := entry.StringTag(protocol.FTFilename); ok {
		result.FileName = name
	}
	if size, ok := entry.UIntTag(protocol.FTFileSize); ok {
		result.FileSize = int64(size)
	}
	if hi, ok := entry.UIntTag(protocol.FTFileSizeHi); ok {
		result.FileSize += int64(hi << 32)
	}
	if sources, ok := entry.UIntTag(protocol.FTSources); ok {
		result.Sources = int(sources)
	}
	if complete, ok := entry.UIntTag(protocol.FTCompleteSources); ok {
		result.CompleteSources = int(complete)
	}
	if bitrate, ok := entry.UIntTag(protocol.FTMediaBitrate); ok {
		result.MediaBitrate = int(bitrate)
	}
	if length, ok := entry.UIntTag(protocol.FTMediaLength); ok {
		result.MediaLength = int(length)
	}
	if codec, ok := entry.StringTag(protocol.FTMediaCodec); ok {
		result.MediaCodec = codec
	}
	if ext, ok := entry.StringTag(protocol.FTFileFormat); ok {
		result.Extension = ext
	}
	if fileType, ok := entry.StringTag(protocol.FTFileType); ok {
		result.FileType = fileType
	}
	result.Note = kadSearchNote(entry)
	return result
}

const kadTagDescription byte = 0x0B

func kadSearchNote(entry kadproto.SearchEntry) string {
	if note, ok := entry.StringTag(kadTagDescription); ok && strings.TrimSpace(note) != "" {
		return note
	}
	if note, ok := entry.StringTag(protocol.FTFileComment); ok && strings.TrimSpace(note) != "" {
		return note
	}
	return ""
}

func normalizeSearchParams(params SearchParams) SearchParams {
	params.Query = strings.TrimSpace(params.Query)
	if params.Scope == 0 {
		params.Scope = SearchScopeAll
	}
	return params
}

// pickKadKeywords splits a query into the distinct tokens worth searching on
// KAD. KAD indexes files under per-word keyword hashes, so a multi-word query
// must be fanned out per token or the remaining tokens never reach the network.
// Longest tokens come first (they are the most discriminating) and the count is
// capped so a single search cannot fan out unbounded.
func pickKadKeywords(query string) []string {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return nil
	}
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return strings.ContainsRune(" ()[]{}<>,._-!?:;\\/\"\t\r\n", r)
	})
	seen := make(map[string]struct{}, len(fields))
	uniq := make([]string, 0, len(fields))
	for _, field := range fields {
		if len([]byte(field)) < 3 {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		uniq = append(uniq, field)
	}
	sort.SliceStable(uniq, func(i, j int) bool {
		return len(uniq[i]) > len(uniq[j])
	})
	if len(uniq) > maxKadKeywords {
		uniq = uniq[:maxKadKeywords]
	}
	return uniq
}

// pickKadKeyword returns the single best keyword for the publish path, which
// files a resource under one keyword hash.
func pickKadKeyword(query string) string {
	keywords := pickKadKeywords(query)
	if len(keywords) == 0 {
		return ""
	}
	return keywords[0]
}
