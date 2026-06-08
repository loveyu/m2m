//go:build android && cgo
// +build android,cgo

package main

/*
#include <jni.h>
#include <stdlib.h>
#include <string.h>

static char* mihomo_jstring_to_c(JNIEnv* env, jstring s) {
    if (s == NULL) {
        return NULL;
    }
    const char* utf = (*env)->GetStringUTFChars(env, s, 0);
    if (utf == NULL) {
        return NULL;
    }
    char* copy = strdup(utf);
    (*env)->ReleaseStringUTFChars(env, s, utf);
    return copy;
}

static int mihomo_jarray_len(JNIEnv* env, jobjectArray array) {
    if (array == NULL) {
        return 0;
    }
    return (*env)->GetArrayLength(env, array);
}

static jstring mihomo_jarray_get(JNIEnv* env, jobjectArray array, int index) {
    return (jstring)(*env)->GetObjectArrayElement(env, array, index);
}

static void mihomo_delete_local_ref(JNIEnv* env, jobject obj) {
    if (obj != NULL) {
        (*env)->DeleteLocalRef(env, obj);
    }
}

static jstring mihomo_new_string(JNIEnv* env, const char* s) {
    return (*env)->NewStringUTF(env, s);
}
*/
import "C"

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/metacubex/mihomo/common/cmd"
	"github.com/metacubex/mihomo/component/geodata"
	"github.com/metacubex/mihomo/component/updater"
	"github.com/metacubex/mihomo/config"
	constant "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/log"

	"go.uber.org/automaxprocs/maxprocs"
)

var androidPluginState = struct {
	sync.Mutex
	running bool
	stopLog func()
}{}

func init() {
	_, _ = maxprocs.Set(maxprocs.Logger(func(string, ...any) {}))
}

//export Java_info_loveyu_mfca_plugin_MihomoPluginCore_nativeGetVersion
func Java_info_loveyu_mfca_plugin_MihomoPluginCore_nativeGetVersion(env *C.JNIEnv, obj C.jobject) C.jstring {
	version := fmt.Sprintf("Mihomo Meta %s android/%s %s %s", constant.Version, runtime.GOARCH, runtime.Version(), constant.BuildTime)
	cVersion := C.CString(version)
	defer C.free(unsafe.Pointer(cVersion))
	return C.mihomo_new_string(env, cVersion)
}

//export Java_info_loveyu_mfca_plugin_MihomoPluginCore_nativeStart
func Java_info_loveyu_mfca_plugin_MihomoPluginCore_nativeStart(
	env *C.JNIEnv,
	obj C.jobject,
	jargs C.jobjectArray,
	jlogFile C.jstring,
) C.jint {
	args := goStringArray(env, jargs)
	logFile := goString(env, jlogFile)

	androidPluginState.Lock()
	defer androidPluginState.Unlock()
	if androidPluginState.running {
		return -2
	}

	stopLog, err := startAndroidPluginLog(logFile)
	if err != nil {
		return -3
	}
	androidPluginState.stopLog = stopLog

	if err := startAndroidPlugin(args); err != nil {
		if androidPluginState.stopLog != nil {
			androidPluginState.stopLog()
			androidPluginState.stopLog = nil
		}
		log.Errorln("android plugin start failed: %s", err.Error())
		return -1
	}
	androidPluginState.running = true
	return 0
}

//export Java_info_loveyu_mfca_plugin_MihomoPluginCore_nativeStop
func Java_info_loveyu_mfca_plugin_MihomoPluginCore_nativeStop(env *C.JNIEnv, obj C.jobject) {
	androidPluginState.Lock()
	if !androidPluginState.running {
		androidPluginState.Unlock()
		return
	}
	androidPluginState.running = false
	stopLog := androidPluginState.stopLog
	androidPluginState.stopLog = nil
	androidPluginState.Unlock()

	executor.Shutdown()
	if stopLog != nil {
		stopLog()
	}
}

//export Java_info_loveyu_mfca_plugin_MihomoPluginCore_nativeIsRunning
func Java_info_loveyu_mfca_plugin_MihomoPluginCore_nativeIsRunning(env *C.JNIEnv, obj C.jobject) C.jboolean {
	androidPluginState.Lock()
	defer androidPluginState.Unlock()
	if androidPluginState.running {
		return C.JNI_TRUE
	}
	return C.JNI_FALSE
}

func startAndroidPlugin(args []string) error {
	net.DefaultResolver.PreferGo = true
	net.DefaultResolver.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		buf := make([]byte, 1024)
		for {
			n := runtime.Stack(buf, true)
			if n < len(buf) {
				buf = buf[:n]
				break
			}
			buf = make([]byte, 2*len(buf))
		}
		return nil, fmt.Errorf("net.DefaultResolver should not be called\n\n%s", buf)
	}

	options, configBytes, postUp, postDown, err := parseAndroidPluginArgs(args)
	if err != nil {
		return err
	}
	if err := hub.Parse(configBytes, options...); err != nil {
		return err
	}
	if updater.GeoAutoUpdate() {
		updater.RegisterGeoUpdater()
	}
	if postUp != "" {
		if _, err := cmd.ExecShell(postUp); err != nil {
			executor.Shutdown()
			return fmt.Errorf("post-up script error: %w", err)
		}
	}
	if postDown != "" {
		log.Warnln("post-down script is ignored by the Android JNI plugin")
	}
	return nil
}

func parseAndroidPluginArgs(args []string) ([]hub.Option, []byte, string, string, error) {
	var (
		homeDir                string
		configFile             string
		configString           string
		externalUI             string
		externalController     string
		externalControllerUnix string
		secret                 string
		postUp                 string
		postDown               string
		geodataMode            bool
	)

	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s requires a value", arg)
			}
			i++
			return args[i], nil
		}
		switch arg {
		case "-d":
			value, err := next()
			if err != nil {
				return nil, nil, "", "", err
			}
			homeDir = value
		case "-f":
			value, err := next()
			if err != nil {
				return nil, nil, "", "", err
			}
			configFile = value
		case "-config":
			value, err := next()
			if err != nil {
				return nil, nil, "", "", err
			}
			configString = value
		case "-ext-ui":
			value, err := next()
			if err != nil {
				return nil, nil, "", "", err
			}
			externalUI = value
		case "-ext-ctl":
			value, err := next()
			if err != nil {
				return nil, nil, "", "", err
			}
			externalController = value
		case "-ext-ctl-unix":
			value, err := next()
			if err != nil {
				return nil, nil, "", "", err
			}
			externalControllerUnix = value
		case "-secret":
			value, err := next()
			if err != nil {
				return nil, nil, "", "", err
			}
			secret = value
		case "-post-up":
			value, err := next()
			if err != nil {
				return nil, nil, "", "", err
			}
			postUp = value
		case "-post-down":
			value, err := next()
			if err != nil {
				return nil, nil, "", "", err
			}
			postDown = value
		case "-m":
			geodataMode = true
		default:
			return nil, nil, "", "", fmt.Errorf("unsupported mihomo plugin arg: %s", arg)
		}
	}

	if homeDir != "" {
		absHome, err := absolutePath(homeDir)
		if err != nil {
			return nil, nil, "", "", err
		}
		homeDir = absHome
		constant.SetHomeDir(homeDir)
	}
	if geodataMode {
		geodata.SetGeodataMode(true)
	}

	var configBytes []byte
	if configString != "" {
		decoded, err := base64.StdEncoding.DecodeString(configString)
		if err != nil {
			return nil, nil, "", "", fmt.Errorf("decode -config: %w", err)
		}
		configBytes = decoded
	} else {
		if configFile != "" {
			absConfig, err := absolutePath(configFile)
			if err != nil {
				return nil, nil, "", "", err
			}
			configFile = absConfig
		} else {
			configFile = filepath.Join(constant.Path.HomeDir(), constant.Path.Config())
		}
		constant.SetConfig(configFile)
		if err := config.Init(constant.Path.HomeDir()); err != nil {
			return nil, nil, "", "", fmt.Errorf("initial configuration directory error: %w", err)
		}
	}

	var options []hub.Option
	if externalUI != "" {
		options = append(options, hub.WithExternalUI(externalUI))
	}
	if externalController != "" {
		options = append(options, hub.WithExternalController(externalController))
	}
	if externalControllerUnix != "" {
		options = append(options, hub.WithExternalControllerUnix(externalControllerUnix))
	}
	if secret != "" {
		options = append(options, hub.WithSecret(secret))
	}

	return options, configBytes, postUp, postDown, nil
}

func startAndroidPluginLog(logFile string) (func(), error) {
	if strings.TrimSpace(logFile) == "" {
		return func() {}, nil
	}
	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	sub := log.Subscribe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for event := range sub {
			_, _ = fmt.Fprintf(file, "%s [%s] %s\n", time.Now().Format(time.RFC3339Nano), event.LogLevel.String(), event.Payload)
		}
	}()
	return func() {
		log.UnSubscribe(sub)
		<-done
		_ = file.Close()
	}, nil
}

func absolutePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is empty")
	}
	if filepath.IsAbs(path) {
		return path, nil
	}
	currentDir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(currentDir, path), nil
}

func goString(env *C.JNIEnv, value C.jstring) string {
	cValue := C.mihomo_jstring_to_c(env, value)
	if cValue == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(cValue))
	return C.GoString(cValue)
}

func goStringArray(env *C.JNIEnv, array C.jobjectArray) []string {
	length := int(C.mihomo_jarray_len(env, array))
	result := make([]string, 0, length)
	for i := 0; i < length; i++ {
		item := C.mihomo_jarray_get(env, array, C.int(i))
		result = append(result, goString(env, item))
		C.mihomo_delete_local_ref(env, C.jobject(item))
	}
	return result
}
