package main

/*
#include <jni.h>

// JNI helpers callable from Go (defined as macros/inlines)
static inline const char* _goGetString(JNIEnv* env, jstring s, jboolean* c) { return (*env)->GetStringUTFChars(env, s, c); }
static inline void _goRelString(JNIEnv* env, jstring s, const char* c) { (*env)->ReleaseStringUTFChars(env, s, c); }
static inline jstring _goNewStr(JNIEnv* env, const char* c) { return (*env)->NewStringUTF(env, c); }
*/
import "C"
import (
	"context"
	"os"
	"time"
	"unsafe"

	"iswitch/internal/p2p"
	"iswitch/internal/server"

	"github.com/libp2p/go-libp2p/core/host"
)

var version = "v1.0.18"

var (
	p2pHost    host.Host
	discovery  *p2p.Discovery
	transferM  *p2p.TransferManager
	httpServer *server.Server
	cancel     context.CancelFunc
)

//export JNI_OnLoad
func JNI_OnLoad(vm *C.JavaVM, reserved unsafe.Pointer) C.jint {
	return C.JNI_VERSION_1_6
}

//export Java_com_iswitch_app_GoLib_nativeStart
func Java_com_iswitch_app_GoLib_nativeStart(env *C.JNIEnv, _ C.jclass, webPort C.jint, p2pPort C.jint, dataDir C.jstring) C.jstring {
	var isCopy C.jboolean
	cstr := C._goGetString(env, dataDir, &isCopy)
	dd := C.GoString(cstr)
	C._goRelString(env, dataDir, cstr)

	os.MkdirAll(dd, 0755)

	h, err := p2p.NewHost(int(p2pPort))
	if err != nil {
		return C._goNewStr(env, C.CString(err.Error()))
	}

	ctx, cancelFn := context.WithCancel(context.Background())
	cancel = cancelFn

	disc := p2p.NewDiscovery(h, false) // LAN-only by default, Android app can toggle via API
	disc.Start(ctx)

	tm := p2p.NewTransferManager(h, dd)
	tm.Start()

	srv := server.NewServer(h, disc, tm, int(webPort), version)
	srv.SetDiscoveryContext(ctx)
	go srv.Start()

	time.Sleep(200 * time.Millisecond)

	p2pHost = h
	discovery = disc
	transferM = tm
	httpServer = srv

	return (C.jstring)(unsafe.Pointer(nil))
}

//export Java_com_iswitch_app_GoLib_nativeStop
func Java_com_iswitch_app_GoLib_nativeStop() {
	if cancel != nil {
		cancel()
	}
	if httpServer != nil {
		httpServer.Stop()
	}
	if p2pHost != nil {
		p2pHost.Close()
	}
}

//export Java_com_iswitch_app_GoLib_nativeGetWebPort
func Java_com_iswitch_app_GoLib_nativeGetWebPort() C.jint {
	if httpServer != nil {
		return C.jint(httpServer.Port())
	}
	return 0
}

func main() {}
