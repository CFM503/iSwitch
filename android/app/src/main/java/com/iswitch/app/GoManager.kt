package com.iswitch.app

import android.content.Context
import android.util.Log
import java.io.File

object GoLib {
    init {
        System.loadLibrary("iswitch")
    }

    external fun nativeStart(webPort: Int, p2pPort: Int, dataDir: String): String?
    external fun nativeStop()
    external fun nativeGetWebPort(): Int
}

class GoManager(private val context: Context) {

    companion object {
        private const val TAG = "GoManager"
        private const val WEB_PORT = 8080
        private const val POLL_INTERVAL_MS = 300L
        private const val MAX_WAIT_MS = 20000L
    }

    private val dataDir: File
        get() = File(context.filesDir, "data").also { it.mkdirs() }

    private var actualPort = WEB_PORT

    fun start(): Boolean {
        try {
            val err = GoLib.nativeStart(WEB_PORT, 0, dataDir.absolutePath)
            if (err != null) {
                Log.e(TAG, "start failed: $err")
                return false
            }
            actualPort = GoLib.nativeGetWebPort()
            if (actualPort <= 0) actualPort = WEB_PORT
            Log.i(TAG, "native server started on port $actualPort")
            return waitForReady()
        } catch (e: Exception) {
            Log.e(TAG, "start failed", e)
            return false
        }
    }

    fun stop() {
        try {
            GoLib.nativeStop()
            Log.i(TAG, "server stopped")
        } catch (e: Exception) {
            Log.e(TAG, "stop error", e)
        }
    }

    fun getWebUrl(): String = "http://127.0.0.1:$actualPort"

    private fun waitForReady(): Boolean {
        val url = getWebUrl()
        val startTime = System.currentTimeMillis()
        while (System.currentTimeMillis() - startTime < MAX_WAIT_MS) {
            try {
                val conn = java.net.URL("$url/api/info").openConnection() as java.net.HttpURLConnection
                conn.connectTimeout = 1000
                conn.readTimeout = 1000
                if (conn.responseCode == 200) {
                    conn.disconnect()
                    Log.i(TAG, "backend ready on $actualPort")
                    return true
                }
                conn.disconnect()
            } catch (_: Exception) {}
            Thread.sleep(POLL_INTERVAL_MS)
        }
        Log.e(TAG, "backend not ready after ${MAX_WAIT_MS}ms")
        return false
    }
}
