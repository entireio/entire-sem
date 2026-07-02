package sem

import (
	"strings"
	"testing"
)

// Kotlin call idioms the generic scanners miss (evidence: on square/okhttp the
// focus method RealWebSocket.failWebSocket resolved all inbound but zero
// outbound CALLS edges): property receivers typed by constructor val/var
// parameters, class-body declarations and factory initializers; trailing
// lambda calls (`taskQueue.execute { ... }`); safe calls (`writer?.close()`);
// and top-level extension functions (`fun Closeable.closeQuietly()`).
func TestKotlinReceiverCallIdioms(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "okhttp3/internal/ws/RealWebSocket.kt", `package okhttp3.internal.ws

import okhttp3.WebSocketListener
import okhttp3.internal.closeQuietly
import okhttp3.internal.concurrent.TaskRunner

class RealWebSocket(
  taskRunner: TaskRunner,
  internal val listener: WebSocketListener,
) {
  /** Used for writes, pings, and close timeouts. */
  private var taskQueue = taskRunner.newQueue()

  /** Null until this web socket is connected. */
  private var writer: WebSocketWriter? = null

  fun failWebSocket(
    e: Exception,
    isWriter: Boolean = false,
  ) {
    val writerToClose: WebSocketWriter?
    synchronized(this) {
      writerToClose = this.writer
      this.writer = null
      if (!isWriter && writerToClose != null) {
        // Trailing lambda: no parentheses after the argument list at all.
        taskQueue.execute {
          writerToClose.closeQuietly()
        }
      }
      taskQueue.shutdown()
    }
    listener.onFailure(this, e)
    writerToClose?.closeQuietly()
  }
}
`)
	writeFile(t, repo, "okhttp3/internal/ws/WebSocketWriter.kt", `package okhttp3.internal.ws

import java.io.Closeable

class WebSocketWriter(
  private val isClient: Boolean,
) : Closeable {
  override fun close() {
  }
}
`)
	writeFile(t, repo, "okhttp3/internal/concurrent/TaskQueue.kt", `package okhttp3.internal.concurrent

class TaskQueue internal constructor(
  internal val taskRunner: TaskRunner,
) {
  fun execute(block: () -> Unit) {
  }

  fun shutdown() {
  }
}
`)
	writeFile(t, repo, "okhttp3/internal/concurrent/TaskRunner.kt", `package okhttp3.internal.concurrent

class TaskRunner {
  fun newQueue(): TaskQueue {
    return TaskQueue(this)
  }
}
`)
	writeFile(t, repo, "okhttp3/WebSocketListener.kt", `package okhttp3

abstract class WebSocketListener {
  open fun onFailure(
    webSocket: RealWebSocket,
    t: Throwable,
  ) {
  }
}
`)
	writeFile(t, repo, "okhttp3/internal/Util.kt", `package okhttp3.internal

import java.io.Closeable
import java.net.ServerSocket

fun Closeable.closeQuietly() {
}

internal fun ServerSocket.closeQuietly() {
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	calls := map[string]RelationRecord{}
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" {
			calls[lastSegment(r.FromID)+"->"+lastSegment(r.ToID)] = r
		}
	}

	// Trailing-lambda call on a property typed by its factory initializer
	// (`private var taskQueue = taskRunner.newQueue()` + the workspace-unique
	// `fun newQueue(): TaskQueue` declared return type).
	if r, ok := calls["RealWebSocket.failWebSocket->TaskQueue.execute"]; !ok || r.Reason != "method call resolved via typed property receiver" {
		t.Fatalf("trailing-lambda property-receiver call not resolved: %#v", calls)
	}
	// Parenthesized call on the same factory-typed property.
	if r, ok := calls["RealWebSocket.failWebSocket->TaskQueue.shutdown"]; !ok || r.Resolution != "type_inferred" {
		t.Fatalf("property-receiver call not resolved: %#v", calls)
	}
	// Property receiver typed by a primary-constructor val parameter, target an
	// abstract class's open method.
	if r, ok := calls["RealWebSocket.failWebSocket->WebSocketListener.onFailure"]; !ok || r.Reason != "method call resolved via typed property receiver" {
		t.Fatalf("constructor-val property-receiver call not resolved: %#v", calls)
	}
	// Safe call (`writerToClose?.closeQuietly()`) on a declared-type local
	// resolving to the Closeable extension function: the receiver's class
	// header spells `: Closeable`, which disambiguates from the ServerSocket
	// overload in the same file.
	var extensionCall RelationRecord
	found := false
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && lastSegment(r.FromID) == "RealWebSocket.failWebSocket" && symbolsByID[r.ToID].Name == "closeQuietly" {
			extensionCall = r
			found = true
		}
	}
	if !found || extensionCall.Reason != "call resolved to Kotlin extension function matching the receiver type" {
		t.Fatalf("extension-function call not resolved: %#v", calls)
	}
	if sig := symbolsByID[extensionCall.ToID].Signature; !strings.Contains(sig, "Closeable.closeQuietly") {
		t.Fatalf("extension call resolved to the wrong overload: %q", sig)
	}
}

// A trailing-lambda call with no receiver (`runTask { ... }`) resolves like any
// bare call; an unknown-typed receiver resolves to an extension function only
// when the name is workspace-unique.
func TestKotlinBareLambdaAndUniqueExtensionCalls(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app/Tasks.kt", `package app

fun runTask(block: () -> Unit) {
}

fun Reporter.flushQuietly() {
}

class Reporter {
  fun report() {
    runTask {
      println("reporting")
    }
  }
}

class Session(
  private val factory: Factory,
) {
  fun close() {
    val reporter = factory.build()
    reporter.flushQuietly()
  }
}

class Factory {
  fun build() = Reporter()
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
	// Bare trailing-lambda call, no parentheses anywhere.
	if _, ok := calls["Reporter.report->runTask"]; !ok {
		t.Fatalf("bare trailing-lambda call not resolved: %#v", calls)
	}
	// `reporter` has no inferable type (the factory method's return type is
	// implicit), so the extension call resolves by workspace-unique name only.
	if r, ok := calls["Session.close->flushQuietly"]; !ok || r.Resolution != "name_only" {
		t.Fatalf("unique extension-function call not resolved: %#v", calls)
	}
}

// Declaration headers, supertype lists, and stdlib scope functions must not
// register as bare trailing-lambda call sites, and text inside comments, string
// templates, and raw strings must not register as call sites at all.
func TestKotlinCallScanPrecision(t *testing.T) {
	content := `package app

class Widget {
  fun render(items: List<String>) {
    items.forEach {
      println(it)
    }
    val label = "count ${items.size} widgets"
    val banner = """
      helper {
      decoy(1)
    """
    // helper { in a comment
    run {
      helper()
    }
  }
}

object Registry {
}

interface Painter {
}

fun helper() {
}
`
	names := kotlinBareLambdaCallIdentifiers(content)
	for _, banned := range []string{"Widget", "Registry", "Painter", "forEach", "run", "helper", "decoy"} {
		if _, ok := names[banned]; ok {
			t.Fatalf("%s wrongly scanned as a bare trailing-lambda call in %v", banned, names)
		}
	}
	callNames := callLikeIdentifiers(stripKotlinCodeText(content), "Kotlin")
	if _, ok := callNames["decoy"]; ok {
		t.Fatalf("raw-string content leaked into call scan: %v", callNames)
	}

	// An anonymous-object supertype (`object : Callback {`) is a type
	// reference, not a call.
	anon := `val cb = object : Callback {
  override fun done() {
  }
}
attach {
  cb.done()
}
`
	names = kotlinBareLambdaCallIdentifiers(anon)
	if _, ok := names["Callback"]; ok {
		t.Fatalf("supertype list wrongly scanned as a call: %v", names)
	}
	if _, ok := names["attach"]; !ok {
		t.Fatalf("bare trailing-lambda call missing: %v", names)
	}
}

// kotlinReceiverCalls accepts `?.`, `!!.`, and trailing-lambda call syntax and
// keeps comment/string text out.
func TestKotlinReceiverCallExtraction(t *testing.T) {
	block := `fun demo() {
    socket?.closeQuietly()
    writer!!.flush()
    taskQueue.execute {
      println("x")
    }
    queue.schedule("$name ping", delay) {
      tick()
    }
    // ghost.call() in a comment
    val s = "text ${user.render()} more"
  }`
	got := map[string]bool{}
	for _, c := range kotlinReceiverCalls(block) {
		got[c.Receiver+"."+c.Method] = true
	}
	for _, want := range []string{"socket.closeQuietly", "writer.flush", "taskQueue.execute", "queue.schedule"} {
		if !got[want] {
			t.Fatalf("missing receiver call %s in %v", want, got)
		}
	}
	for _, banned := range []string{"ghost.call", "user.render"} {
		if got[banned] {
			t.Fatalf("comment/string call %s wrongly extracted: %v", banned, got)
		}
	}
}

// kotlinPropertyTypes reads constructor val/var parameters, modifier-prefixed
// typed declarations, constructor initializers, and factory initializers with
// workspace-unique return types; conflicting bindings are dropped and locals
// (no modifier) never contribute.
func TestKotlinPropertyTypes(t *testing.T) {
	content := `class RealWebSocket(
  taskRunner: TaskRunner,
  internal val listener: WebSocketListener,
  private val random: Random,
  minimumDeflateSize: Long,
) : WebSocket {
  private var taskQueue = taskRunner.newQueue()
  private var writer: WebSocketWriter? = null
  internal val lock = ReentrantLock()
  private var mystery = unknownFactory()
  private val conflicted: Reader? = null

  fun helper() {
    val local: LocalOnly = build()
  }
}

class Other {
  private val conflicted: Writer? = null
}
`
	returnTypes := map[string]map[string][]string{
		"newQueue": {"TaskRunner.kt": {"TaskQueue"}},
	}
	types := kotlinPropertyTypes(content, returnTypes)
	want := map[string]string{
		"listener":  "WebSocketListener",
		"random":    "Random",
		"taskQueue": "TaskQueue",
		"writer":    "WebSocketWriter",
		"lock":      "ReentrantLock",
	}
	for name, typeName := range want {
		if types[name] != typeName {
			t.Fatalf("property %s: got %q, want %q (all: %v)", name, types[name], typeName, types)
		}
	}
	for _, banned := range []string{"mystery", "conflicted", "local", "minimumDeflateSize"} {
		if _, ok := types[banned]; ok {
			t.Fatalf("property %s should not be typed: %v", banned, types)
		}
	}
}

// kotlinLocalVarTypes reads declared-type local declarations, dropping names
// declared with two different types.
func TestKotlinLocalVarTypes(t *testing.T) {
	block := `fun failWebSocket(e: Exception) {
    val writerToClose: WebSocketWriter?
    var socketToCancel: okio.Socket? = null
    val twice: Reader = open()
    val twice: Writer = openOther()
    val lower: string = ""
  }`
	types := kotlinLocalVarTypes(block)
	if types["writerToClose"] != "WebSocketWriter" || types["socketToCancel"] != "Socket" {
		t.Fatalf("declared-type locals not extracted: %v", types)
	}
	if _, ok := types["twice"]; ok {
		t.Fatalf("conflicting declaration should be dropped: %v", types)
	}
	if _, ok := types["lower"]; ok {
		t.Fatalf("lowercase type should be skipped: %v", types)
	}
}

// Extension receiver parsing and supertype-directed matching.
func TestKotlinExtensionHelpers(t *testing.T) {
	if got := kotlinExtensionReceiver("fun Closeable.closeQuietly()", "closeQuietly"); got != "Closeable" {
		t.Fatalf("extension receiver: got %q", got)
	}
	if got := kotlinExtensionReceiver("internal fun java.net.Socket.closeQuietly()", "closeQuietly"); got != "Socket" {
		t.Fatalf("qualified extension receiver: got %q", got)
	}
	if got := kotlinExtensionReceiver("fun <T> MutableList<T>.readOnly(): List<T>", "readOnly"); got != "MutableList" {
		t.Fatalf("generic extension receiver: got %q", got)
	}
	if got := kotlinExtensionReceiver("fun closeQuietly()", "closeQuietly"); got != "" {
		t.Fatalf("plain function misread as extension: %q", got)
	}
	supers := kotlinSupertypeNames("class WebSocketWriter( private val isClient: Boolean, val sink: BufferedSink, ) : Closeable")
	if len(supers) != 1 || supers[0] != "Closeable" {
		t.Fatalf("supertypes: %v", supers)
	}
	supers = kotlinSupertypeNames("class RealWebSocket( taskRunner: TaskRunner, ) : WebSocket, WebSocketReader.FrameCallback, Lockable")
	if len(supers) != 3 || supers[0] != "WebSocket" || supers[1] != "FrameCallback" || supers[2] != "Lockable" {
		t.Fatalf("supertypes: %v", supers)
	}
	if supers := kotlinSupertypeNames("class TaskQueue internal constructor( internal val taskRunner: TaskRunner, )"); supers != nil {
		t.Fatalf("no supertypes expected: %v", supers)
	}
}

// Implicit-extension-receiver call idioms (evidence: on ktor the focus
// extension function ApplicationCall.resolveResource resolved 0/4 inbound
// edges): a bare call inside a `fun R.name(...)` body dispatches on the
// extension receiver R; a lambda parameter is typed by the callee's declared
// function-type parameter; a bare receiver inside an extension body may be a
// property of R declared in another file; and `base.property.method(...)`
// chains type the terminal call through the property's declared type.
func TestKotlinImplicitExtensionReceiverCalls(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "server/PipelineCall.kt", `package server

public interface ApplicationCall : CoroutineScope {
    public val application: Application
}
`)
	writeFile(t, repo, "server/Application.kt", `package server

public class Application {
}
`)
	writeFile(t, repo, "server/StaticContentResolution.kt", `package server

public fun ApplicationCall.resolveResource(
    path: String,
    resourcePackage: String? = null
): ReadChannelContent? {
    return null
}

internal fun Application.resolveResource(
    path: String,
    mimeResolve: (URL) -> ContentType
): ReadChannelContent? {
    return null
}
`)
	writeFile(t, repo, "server/ApplicationResponseFunctionsJvm.kt", `package server

public suspend fun ApplicationCall.respondResource(resourcePath: String) {
    val message = resolveResource(resourcePath) ?: throw IllegalArgumentException(
        "Resource not found: $resourcePath"
    )
}
`)
	writeFile(t, repo, "server/StatusPages.kt", `package server

public class StatusPagesConfig {
    public fun status(
        vararg status: HttpStatusCode,
        handler: suspend (ApplicationCall, HttpStatusCode) -> Unit
    ) {
    }

    public fun status(
        vararg status: HttpStatusCode,
        handler: suspend StatusContext.(HttpStatusCode) -> Unit
    ) {
    }
}

public class HttpStatusCode {
}

public class StatusContext {
}
`)
	writeFile(t, repo, "server/Decoy.kt", `package server

class Monitor {
    fun status(handler: (Monitor) -> Unit) {
    }
}
`)
	writeFile(t, repo, "server/StatusPagesJvm.kt", `package server

public fun StatusPagesConfig.statusFile(vararg code: HttpStatusCode, filePattern: String) {
    status(*code) { call, status ->
        val message = call.resolveResource(filePattern)
    }
}
`)
	writeFile(t, repo, "server/PreCompressed.kt", `package server

internal fun bestCompressionFit(
    call: ApplicationCall,
    resource: String,
    contentType: (URL) -> ContentType
): CompressedResource? {
    val resolved = call.application.resolveResource(resource) { url ->
        contentType(url)
    }
    return null
}

internal suspend fun ApplicationCall.respondStaticResource(requestedResource: String) {
    val content = application.resolveResource(
        path = requestedResource,
        mimeResolve = contentType
    )
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	// resolveResourceTarget returns the resolveResource overload the caller
	// resolved to, identified by its declared extension receiver.
	resolveResourceTarget := func(caller string) string {
		for _, r := range snapshot.Relations {
			if r.Type != "CALLS" || !strings.Contains(r.FromID, ":"+caller) {
				continue
			}
			to := symbolsByID[r.ToID]
			if to.Name != "resolveResource" {
				continue
			}
			return kotlinExtensionReceiver(to.Signature, "resolveResource")
		}
		return ""
	}
	// Bare call in an extension body resolves type-directed to the
	// ApplicationCall overload, not the Application one.
	if got := resolveResourceTarget("respondResource"); got != "ApplicationCall" {
		t.Fatalf("respondResource bare call resolved to receiver %q, want ApplicationCall", got)
	}
	// Lambda parameter `call` typed by the callee's declared function-type
	// parameter; the callee itself resolves as a member of the enclosing
	// extension receiver despite the same-named decoy method.
	if got := resolveResourceTarget("statusFile"); got != "ApplicationCall" {
		t.Fatalf("statusFile lambda-param call resolved to receiver %q, want ApplicationCall", got)
	}
	statusMemberCall := false
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && strings.Contains(r.FromID, ":statusFile") && symbolsByID[r.ToID].QualifiedName == "StatusPagesConfig.status" {
			statusMemberCall = true
		}
	}
	if !statusMemberCall {
		t.Fatalf("statusFile -> StatusPagesConfig.status member call not resolved")
	}
	// Chained receiver `call.application.resolveResource(...)`: the property
	// hop is typed by the cross-file interface property field symbol.
	if got := resolveResourceTarget("bestCompressionFit"); got != "Application" {
		t.Fatalf("bestCompressionFit chained call resolved to receiver %q, want Application", got)
	}
	// Bare receiver `application` inside an ApplicationCall extension body is
	// this.application, typed by the same cross-file property.
	if got := resolveResourceTarget("respondStaticResource"); got != "Application" {
		t.Fatalf("respondStaticResource property-receiver call resolved to receiver %q, want Application", got)
	}
}

// Kotlin `fun interface`, modifier-prefixed interface declarations, and
// interface-body properties must all survive into the symbol inventory
// (evidence: ktor's HttpClientEngine/HttpClientPlugin and okhttp's Interceptor
// were absent or misclassified in the snapshots).
func TestKotlinInterfaceDeclarationKinds(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "client/HttpClientEngine.kt", `package client

public interface HttpClientEngine : CoroutineScope, Closeable {
    public val dispatcher: CoroutineDispatcher

    @InternalAPI
    public suspend fun execute(data: HttpRequestData): HttpResponseData
}

@OptIn(InternalAPI::class)
public sealed interface Marker {
}
`)
	writeFile(t, repo, "client/Interceptor.kt", `package client

fun interface Interceptor {
  fun intercept(chain: Chain): Response

  interface Chain {
    fun proceed(request: Request): Response
  }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, s := range snapshot.Symbols {
		kinds[s.QualifiedName] = s.Kind
	}
	for _, name := range []string{"HttpClientEngine", "Marker", "Interceptor", "Chain"} {
		if kinds[name] != "interface" {
			t.Fatalf("kind of %s = %q, want interface (all symbols: %v)", name, kinds[name], kinds)
		}
	}
	// The fun interface's member parses inside the container again.
	if kinds["Interceptor.intercept"] != "method" {
		t.Fatalf("Interceptor.intercept kind = %q, want method", kinds["Interceptor.intercept"])
	}
	// Interface-body properties become field symbols with their declared type.
	found := false
	for _, s := range snapshot.Symbols {
		if s.QualifiedName == "HttpClientEngine.dispatcher" && s.Kind == "field" && strings.Contains(s.Signature, "CoroutineDispatcher") {
			found = true
		}
	}
	if !found {
		t.Fatalf("interface-body property HttpClientEngine.dispatcher not extracted as a typed field")
	}
}
