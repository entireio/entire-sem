package sem

import (
	"strings"
	"testing"
)

// Swift call idioms the generic scanners miss (evidence: on apple/swift-nio the
// focus method ByteBuffer.discardReadBytes resolved 0/3 inbound CALLS edges):
// labeled/inout parameters (`remainder buffer: inout ByteBuffer`), enum-case
// pattern bindings dispatched inside a defer block (`case .available(var
// buffer):` ... `defer { buffer.discardReadBytes() }`), and force-unwrapped
// optional stored-property receivers (`self._buffer!.discardReadBytes()`).
func TestSwiftReceiverTypedCallIdioms(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Sources/NIOCore/ByteBuffer-core.swift", `public struct ByteBuffer {
    internal var _readerIndex: Int = 0

    @discardableResult
    public mutating func discardReadBytes() -> Bool {
        guard self._readerIndex > 0 else {
            return false
        }
        return true
    }
}
`)
	writeFile(t, repo, "Sources/NIOCore/Codec.swift", `struct B2MDBuffer {
    enum BufferAvailability {
        case bufferAlreadyBeingProcessed
        case nothingAvailable
        case available(ByteBuffer)
    }

    private var buffers: [ByteBuffer] = []

    func startProcessing(allowEmptyBuffer: Bool) -> BufferAvailability {
        return .nothingAvailable
    }

    mutating func finishProcessing(remainder buffer: inout ByteBuffer) {
        if buffer.readableBytes == 0 && self.buffers.isEmpty {
            return
        }
        buffer.discardReadBytes()
    }
}

final class ByteToMessageHandler {
    private var buffer = B2MDBuffer()

    private func withNextBuffer(allowEmptyBuffer: Bool) -> Bool {
        switch self.buffer.startProcessing(allowEmptyBuffer: allowEmptyBuffer) {
        case .available(var buffer):
            var possiblyReclaimBytes = false
            defer {
                if possiblyReclaimBytes {
                    buffer.discardReadBytes()
                }
                self.buffer.finishProcessing(remainder: &buffer)
            }
            possiblyReclaimBytes = true
            return true
        default:
            return false
        }
    }

    func drain(_ buffer: inout ByteBuffer) {
        buffer.discardReadBytes()
    }

    func reclaimPending() {
        var pending: ByteBuffer? = nil
        pending!.discardReadBytes()
    }

    func makeAndDrain() {
        var scratch = ByteBuffer()
        scratch.discardReadBytes()
    }
}

extension ByteToMessageHandler {
    func debugDescription(buffer: ByteBuffer) -> String {
        let text = """
            buffer.discardReadBytes()
            leaked(
            """
        return text
    }

    func summary(buffer: ByteBuffer, count: Int) -> String {
        return "buffer.discardReadBytes() ran \(count) times"
    }
}
`)
	writeFile(t, repo, "Sources/NIOCore/Processor.swift", `public protocol ByteDecoder {
    func didDecode(message: String)
    func shouldReclaim() -> Bool
}

extension ByteDecoder {
    public func shouldReclaim() -> Bool {
        return true
    }
}

final class DefaultDelegate: ByteDecoder {
    func didDecode(message: String) {
    }
}

final class Processor {
    internal private(set) var _buffer: ByteBuffer?
    weak var delegate: ByteDecoder?

    func _postDecodeCheck() {
        if self.delegate!.shouldReclaim() {
            self._buffer!.discardReadBytes()
        }
        delegate?.didDecode(message: "done")
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	calls := map[string]RelationRecord{}
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" {
			calls[lastSegment(r.FromID)+"->"+lastSegment(r.ToID)] = r
		}
	}

	// Labeled inout parameter (`remainder buffer: inout ByteBuffer`): no branch
	// of the generic parameterVarTypes understands the argument label.
	if r, ok := calls["B2MDBuffer.finishProcessing->ByteBuffer.discardReadBytes"]; !ok || r.Reason != "method call resolved via typed parameter receiver" {
		t.Fatalf("labeled inout parameter receiver not resolved: %#v", calls)
	}
	// Underscore-labeled inout parameter (`_ buffer: inout ByteBuffer`).
	if r, ok := calls["ByteToMessageHandler.drain->ByteBuffer.discardReadBytes"]; !ok || r.Reason != "method call resolved via typed parameter receiver" {
		t.Fatalf("underscore-labeled parameter receiver not resolved: %#v", calls)
	}
	// Enum-case pattern binding (`case .available(var buffer):`, payload typed
	// by the same file's `case available(ByteBuffer)` declaration), with the
	// call sitting inside a defer block.
	if r, ok := calls["ByteToMessageHandler.withNextBuffer->ByteBuffer.discardReadBytes"]; !ok || r.Resolution != "type_inferred" {
		t.Fatalf("enum-case binding receiver (defer block) not resolved: %#v", calls)
	}
	// Declared-type optional local with a force-unwrapped call
	// (`var pending: ByteBuffer? = nil` ... `pending!.discardReadBytes()`).
	if r, ok := calls["ByteToMessageHandler.reclaimPending->ByteBuffer.discardReadBytes"]; !ok || r.Resolution != "type_inferred" {
		t.Fatalf("declared-type local with force-unwrap not resolved: %#v", calls)
	}
	// Constructor-initialized local (`var scratch = ByteBuffer()`), the
	// pre-existing generic tier: must keep working alongside the Swift ones.
	if _, ok := calls["ByteToMessageHandler.makeAndDrain->ByteBuffer.discardReadBytes"]; !ok {
		t.Fatalf("constructor-initialized local receiver not resolved: %#v", calls)
	}
	// Force-unwrapped optional stored property
	// (`internal private(set) var _buffer: ByteBuffer?` +
	// `self._buffer!.discardReadBytes()`).
	if r, ok := calls["Processor._postDecodeCheck->ByteBuffer.discardReadBytes"]; !ok || r.Reason != "method call resolved via typed property receiver" {
		t.Fatalf("optional stored-property receiver not resolved: %#v", calls)
	}
	// Protocol-typed property calling a requirement that has an extension
	// default: resolves to the protocol's own method symbol.
	if r, ok := calls["Processor._postDecodeCheck->ByteDecoder.shouldReclaim"]; !ok || r.Reason != "method call resolved via typed property receiver" {
		t.Fatalf("protocol-typed property receiver (default impl) not resolved: %#v", calls)
	}
	// Protocol-typed property calling a bodyless requirement (no method symbol
	// on the protocol): resolves to the unique implementing method, like the Go
	// interface fallback. Also exercises the `?.` optional-chained receiver.
	if r, ok := calls["Processor._postDecodeCheck->DefaultDelegate.didDecode"]; !ok || r.Reason != "protocol-typed receiver call resolved to the unique implementing method" || r.Resolution != "name_only" {
		t.Fatalf("protocol requirement not resolved to unique implementing method: %#v", calls)
	}
	// Multiline string bodies must not register call sites: debugDescription's
	// typed `buffer` parameter plus the leaked `buffer.discardReadBytes()` text
	// would otherwise produce a confident false edge.
	if _, ok := calls["ByteToMessageHandler.debugDescription->ByteBuffer.discardReadBytes"]; ok {
		t.Fatalf("multiline string body leaked a call site: %#v", calls)
	}
	// Single-line strings (including interpolation segments) likewise.
	if _, ok := calls["ByteToMessageHandler.summary->ByteBuffer.discardReadBytes"]; ok {
		t.Fatalf("single-line string body leaked a call site: %#v", calls)
	}
}

func TestSwiftParameterVarTypes(t *testing.T) {
	cases := []struct {
		signature string
		name      string
		want      map[string]string
	}{
		{
			signature: "mutating func finishProcessing(remainder buffer: inout ByteBuffer)",
			name:      "finishProcessing",
			want:      map[string]string{"buffer": "ByteBuffer"},
		},
		{
			signature: "func drain(_ buffer: ByteBuffer)",
			name:      "drain",
			want:      map[string]string{"buffer": "ByteBuffer"},
		},
		{
			signature: "func write(to target: ByteBuffer = ByteBuffer(), flush: Bool)",
			name:      "write",
			want:      map[string]string{"target": "ByteBuffer", "flush": "Bool"},
		},
		{
			// Attributes and ownership modifiers are skipped; function-type
			// parameters yield nothing.
			signature: "@inlinable public func process(_ body: (inout ByteBuffer) throws -> Void, on loop: EventLoop, count: Int) rethrows",
			name:      "process",
			want:      map[string]string{"loop": "EventLoop", "count": "Int"},
		},
		{
			// Qualified and generic types collapse to the terminal segment.
			signature: "func enqueue(state: B2MDBuffer.BufferAvailability, buffers: CircularBuffer<ByteBuffer>)",
			name:      "enqueue",
			want:      map[string]string{"state": "BufferAvailability", "buffers": "CircularBuffer"},
		},
		{
			signature: "init(wrapping buffer: ByteBuffer)",
			name:      "init",
			want:      map[string]string{"buffer": "ByteBuffer"},
		},
		{
			// The parens belonging to an attribute before the func keyword must
			// not be mistaken for the parameter list.
			signature: "@available(*, deprecated) func flush(buffer b: ByteBuffer)",
			name:      "flush",
			want:      map[string]string{"b": "ByteBuffer"},
		},
	}
	for _, tc := range cases {
		got := swiftParameterVarTypes(tc.signature, tc.name)
		if len(got) != len(tc.want) {
			t.Fatalf("swiftParameterVarTypes(%q) = %#v, want %#v", tc.signature, got, tc.want)
		}
		for name, typeName := range tc.want {
			if got[name] != typeName {
				t.Fatalf("swiftParameterVarTypes(%q)[%s] = %q, want %q", tc.signature, name, got[name], typeName)
			}
		}
	}
}

func TestSwiftLocalVarTypes(t *testing.T) {
	payloads := map[string]string{"available": "ByteBuffer"}
	block := `
        var pending: ByteBuffer? = nil
        let loop: EventLoop
        switch self.buffer.startProcessing() {
        case .available(var buffer):
            buffer.discardReadBytes()
        case .nothingAvailable:
            break
        }
        if case let .available(remainder) = state {
            remainder.discardReadBytes()
        }
        // A name bound to two different types is dropped.
        let twice: ByteBuffer? = nil
        let twice: EventLoop? = nil
        // Non-capitalized annotations never bind.
        let flag: eventKind = .none
`
	got := swiftLocalVarTypes(block, payloads)
	want := map[string]string{
		"pending":   "ByteBuffer",
		"loop":      "EventLoop",
		"buffer":    "ByteBuffer",
		"remainder": "ByteBuffer",
	}
	if len(got) != len(want) {
		t.Fatalf("swiftLocalVarTypes = %#v, want %#v", got, want)
	}
	for name, typeName := range want {
		if got[name] != typeName {
			t.Fatalf("swiftLocalVarTypes[%s] = %q, want %q", name, got[name], typeName)
		}
	}
}

func TestSwiftFileTypeInfo(t *testing.T) {
	content := `struct B2MDBuffer {
    enum BufferAvailability {
        case bufferAlreadyBeingProcessed
        case nothingAvailable
        case available(ByteBuffer)
    }

    enum Wrapped {
        case labeled(buffer: ByteBuffer)
        case pair(ByteBuffer, Int)
    }

    internal private(set) var _buffer: ByteBuffer?
    weak var delegate: ByteDecoder?
    private var buffers = CircularBuffer<ByteBuffer>(initialCapacity: 4)
    // No modifier: could be a method-body local, so it never binds.
    var state: State = .ready

    func startProcessing() -> BufferAvailability {
        // A switch pattern spells the case with a leading dot and never
        // registers as a payload declaration.
        let local: Int = 0
        return .nothingAvailable
    }
}

final class Other {
    // Same property name with a different type: dropped, not guessed.
    internal var _buffer: EventLoop?

    // Computed property in a type body: the brace body marks it a
    // property even without modifiers.
    var mediaType: HTTPMediaType {
        return .plainText
    }
}

extension Request {
    // Computed property in an extension block.
    public var fileio: FileIO {
        return .init(request: self)
    }
}

protocol RequestProvider {
    // Protocol property requirement.
    var currentRequest: Request { get }
}
`
	info := swiftFileTypeInfo(content)
	if info.props["delegate"] != "ByteDecoder" || info.props["buffers"] != "CircularBuffer" {
		t.Fatalf("props = %#v", info.props)
	}
	if info.props["mediaType"] != "HTTPMediaType" || info.props["fileio"] != "FileIO" || info.props["currentRequest"] != "Request" {
		t.Fatalf("computed properties not collected: %#v", info.props)
	}
	if _, ok := info.props["_buffer"]; ok {
		t.Fatalf("conflicting property type not dropped: %#v", info.props)
	}
	if _, ok := info.props["state"]; ok {
		t.Fatalf("unmodified declaration bound as property: %#v", info.props)
	}
	if _, ok := info.props["local"]; ok {
		t.Fatalf("method-body local bound as property: %#v", info.props)
	}
	if info.enumPayloads["available"] != "ByteBuffer" || info.enumPayloads["labeled"] != "ByteBuffer" {
		t.Fatalf("enumPayloads = %#v", info.enumPayloads)
	}
	if _, ok := info.enumPayloads["pair"]; ok {
		t.Fatalf("multi-payload case bound: %#v", info.enumPayloads)
	}
	if _, ok := info.enumPayloads["nothingAvailable"]; ok {
		t.Fatalf("payload-less case bound: %#v", info.enumPayloads)
	}
}

func TestSwiftReceiverCallsOperators(t *testing.T) {
	block := `
        self._buffer!.discardReadBytes()
        delegate?.didDecode(message: "done")
        buffer.write(bytes)
        let text = """
            masked.leakedCall()
            """
`
	calls := swiftReceiverCalls(block)
	byKey := map[string]receiverCall{}
	for _, c := range calls {
		byKey[c.Receiver+"."+c.Method] = c
	}
	if _, ok := byKey["_buffer.discardReadBytes"]; !ok {
		t.Fatalf("force-unwrapped receiver missed: %#v", calls)
	}
	if _, ok := byKey["delegate.didDecode"]; !ok {
		t.Fatalf("optional-chained receiver missed: %#v", calls)
	}
	if _, ok := byKey["buffer.write"]; !ok {
		t.Fatalf("plain receiver missed: %#v", calls)
	}
	if _, ok := byKey["masked.leakedCall"]; ok {
		t.Fatalf("multiline string body leaked a call site: %#v", calls)
	}
	if strings.Contains(stripSwiftCodeText(block), "leakedCall") {
		t.Fatalf("stripSwiftCodeText left multiline string body intact")
	}
}

// Swift property-chain receivers (evidence: on vapor/vapor the focus method
// FileIO.streamFile resolved 0/5 inbound CALLS edges): every caller spells
// `request.fileio.streamFile(...)` / `req.fileio.streamFile(...)`, where
// `fileio` is a computed property declared in `extension Request` — in the
// callee's file, not the callers' — and `req` is an unannotated route-handler
// closure parameter. The outbound edges from streamFile hop through locals
// bound from property chains (`if let firstRange = contentRange.ranges.first`)
// into extension methods on dotted nested types (HTTPFields.Range.Value).
func TestSwiftPropertyChainReceiverCalls(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Sources/Vapor/Request/Request.swift", `public final class Request: Sendable {
    public var headers: HTTPFields {
        get { self.requestBox.headers }
        set { self.requestBox.headers = newValue }
    }

    public var url: URI = .init()
}
`)
	writeFile(t, repo, "Sources/Vapor/Utilities/FileIO.swift", `extension Request {
    public var fileio: FileIO {
        return .init(request: self)
    }
}

public struct FileIO: Sendable {
    let request: Request

    public func streamFile(
        at path: String,
        mediaType: HTTPMediaType? = nil,
        advancedETagComparison: Bool = false,
        onCompleted: @escaping @Sendable (Result<Void, any Error>) async throws -> () = { _ in }
    ) async throws -> Response {
        let contentRange: HTTPFields.Range?
        if let rangeFromHeaders = request.headers.range {
            contentRange = rangeFromHeaders
        } else {
            contentRange = nil
        }
        let response = Response(status: .ok)
        if let contentRange = contentRange {
            if let firstRange = contentRange.ranges.first {
                let range = try firstRange.asResponseContentRange(limit: 100)
                let text = firstRange.serialize()
                response.headers.contentRange = HTTPFields.ContentRange(range: range, text: text)
            }
        }
        if
            let fileExtension = path.components(separatedBy: ".").last,
            let type = mediaType ?? HTTPMediaType.fileExtension(fileExtension)
        {
            response.headers.contentType = type
        }
        return response
    }
}
`)
	writeFile(t, repo, "Sources/Vapor/HTTP/HTTPFields+ContentRange.swift", `extension HTTPFields {
    public struct Range: Sendable, Equatable {
        public let unit: RangeUnit
        public let ranges: [HTTPFields.Range.Value]

        public init(unit: RangeUnit, ranges: [HTTPFields.Range.Value]) {
            self.unit = unit
            self.ranges = ranges
        }
    }

    public struct ContentRange: Equatable {
        public let range: HTTPFields.ContentRange.Value
    }

    public var range: Range? {
        get {
            return HTTPFields.Range(directives: self.parseDirectives(name: .range))
        }
        set {
            self[.range] = newValue.serialize()
        }
    }
}

extension HTTPFields.Range {
    public enum Value: Sendable, Equatable {
        case start(value: Int)
        case tail(value: Int)

        public func serialize() -> String {
            return ""
        }
    }
}

extension HTTPFields.ContentRange {
    public enum Value: Equatable {
        case within(start: Int, end: Int)

        public func serialize() -> String {
            return ""
        }
    }
}

extension HTTPFields.Range.Value {
    public func asResponseContentRange(limit: Int) throws -> HTTPFields.ContentRange.Value {
        return .within(start: 0, end: limit)
    }
}
`)
	writeFile(t, repo, "Sources/Vapor/HTTP/HTTPMediaType.swift", `public struct HTTPMediaType: Sendable, Equatable {
    public let type: String
    public let subType: String

    public static func fileExtension(_ ext: String) -> HTTPMediaType? {
        fileExtensionMediaTypeMapping[ext]
    }
}

let fileExtensionMediaTypeMapping: [String: HTTPMediaType] = [:]
`)
	writeFile(t, repo, "Sources/Vapor/Middleware/FileMiddleware.swift", `public final class FileMiddleware {
    private let publicDirectory: String

    public func respond(to request: Request, chainingTo next: any Responder) async throws -> Response {
        let absPath = self.publicDirectory + request.url.path
        return try await request
            .fileio
            .streamFile(at: absPath, advancedETagComparison: true)
            .cachePolicy(cachePolicy)
    }
}
`)
	writeFile(t, repo, "Tests/VaporTests/FileTests.swift", `import Testing

struct FileTests {
    @Test("Test Stream File")
    func testStreamFile() async throws {
        try await withApp { app in
            app.get("file-stream") { req -> Response in
                return try await req.fileio.streamFile(at: #filePath, advancedETagComparison: true) { result in
                    do {
                        try result.get()
                    } catch {
                        Issue.record("File Stream should have succeeded")
                    }
                }
            }
        }
    }

    @Test("Test Stream File Trailing Closure")
    func testStreamFileTrailingClosure() async throws {
        try await withApp { app in
            app.get("file-stream") { req -> Response in
                return try await req.fileio!.streamFile { result in
                    try result.get()
                }
            }
        }
    }

    @Test("Wrong-typed base never falls back to the unique property")
    func testWrongBase() async throws {
        let media = HTTPMediaType(type: "text", subType: "plain")
        _ = media.fileio.streamFile(at: "x")
    }

    @Test("Deep chains are skipped, not guessed")
    func testDeepChain() async throws {
        let holder = Holder()
        _ = try await holder.request.fileio.streamFile(at: "x")
    }
}

struct Holder {
    let request: Request

    init() {}
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	calls := map[string]RelationRecord{}
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" {
			calls[lastSegment(r.FromID)+"->"+lastSegment(r.ToID)] = r
		}
	}

	// Typed-base chain across files, spelled over several lines with a further
	// call after the tail: `request\n.fileio\n.streamFile(...)\n.cachePolicy(...)`.
	// `fileio` is a computed property declared in `extension Request` in the
	// callee's file.
	if r, ok := calls["FileMiddleware.respond->FileIO.streamFile"]; !ok || r.Reason != "method call resolved via typed property of chained receiver" || r.Resolution != "type_inferred" {
		t.Fatalf("typed-base property chain not resolved: %#v", calls)
	}
	// Untyped base (route-handler closure parameter `req`): the hop resolves
	// through the workspace-unique `fileio` property, gated by streamFile
	// existing on its declared type.
	if r, ok := calls["FileTests.testStreamFile->FileIO.streamFile"]; !ok || r.Reason != "chained receiver typed via workspace-unique Swift property" || r.Resolution != "name_only" {
		t.Fatalf("unique-property chain (untyped base) not resolved: %#v", calls)
	}
	// Force-unwrapped hop with a bare trailing closure: `req.fileio!.streamFile { ... }`.
	if _, ok := calls["FileTests.testStreamFileTrailingClosure->FileIO.streamFile"]; !ok {
		t.Fatalf("trailing-closure chain not resolved: %#v", calls)
	}
	// Local bound from a property chain with an array `first` hop
	// (`if let firstRange = contentRange.ranges.first`), calling an extension
	// method on a dotted nested type (HTTPFields.Range.Value): resolves by the
	// method's unique qualified name.
	if r, ok := calls["FileIO.streamFile->HTTPFields.Range.Value.asResponseContentRange"]; !ok || r.Reason != "method call resolved via nested-type property-chain local receiver" {
		t.Fatalf("nested-type chain-bound local not resolved: %#v", calls)
	}
	// Static call on the named type keeps resolving (regression guard for the
	// second vapor outbound miss).
	if _, ok := calls["FileIO.streamFile->HTTPMediaType.fileExtension"]; !ok {
		t.Fatalf("static call on named type not resolved: %#v", calls)
	}
	// `firstRange.serialize()` is ambiguous by short qualified name (both
	// nested Value enums declare serialize): resolved to nothing, not guessed.
	if _, ok := calls["FileIO.streamFile->Value.serialize"]; ok {
		t.Fatalf("ambiguous nested-type method guessed: %#v", calls)
	}
	// A typed base whose type does not declare the property never falls back
	// to the workspace-unique property name.
	if _, ok := calls["FileTests.testWrongBase->FileIO.streamFile"]; ok {
		t.Fatalf("wrong-typed base fell back to unique property: %#v", calls)
	}
	// Deep chains (`holder.request.fileio.streamFile(...)`) are skipped.
	if _, ok := calls["FileTests.testDeepChain->FileIO.streamFile"]; ok {
		t.Fatalf("deep chain guessed: %#v", calls)
	}
}

func TestSwiftChainedReceiverCalls(t *testing.T) {
	block := `
        return try await request
            .fileio
            .streamFile(at: absPath, advancedETagComparison: comparison)
            .cachePolicy(cachePolicy)
        req.fileio?.streamFile { result in }
        socket!.channel.close(promise: nil)
        res.body.string.contains(test)
        f().handler.run()
        let text = """
            masked.prop.leakedCall()
            """
`
	chains := swiftChainedReceiverCalls(block)
	byDetail := map[string]swiftChainedCall{}
	for _, c := range chains {
		byDetail[c.Detail] = c
	}
	if _, ok := byDetail["request.fileio.streamFile"]; !ok {
		t.Fatalf("multi-line chain missed: %#v", chains)
	}
	if _, ok := byDetail["req.fileio.streamFile"]; !ok {
		t.Fatalf("optional-chained trailing-closure chain missed: %#v", chains)
	}
	if _, ok := byDetail["socket.channel.close"]; !ok {
		t.Fatalf("force-unwrapped chain missed: %#v", chains)
	}
	for detail := range byDetail {
		switch {
		case strings.Contains(detail, "contains"):
			t.Fatalf("deep chain tail bound as a chain: %#v", chains)
		case strings.Contains(detail, "run"):
			t.Fatalf("call-result receiver bound as a chain base: %#v", chains)
		case strings.Contains(detail, "leakedCall"):
			t.Fatalf("multiline string body leaked a chain site: %#v", chains)
		}
	}
}
