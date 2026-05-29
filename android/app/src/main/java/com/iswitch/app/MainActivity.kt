package com.iswitch.app

import android.app.Activity
import android.content.ContentValues
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Environment
import android.provider.MediaStore
import android.util.Log
import android.view.View
import android.webkit.*
import android.widget.Button
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.FileProvider
import androidx.webkit.WebViewCompat
import kotlinx.coroutines.*
import java.io.File
import java.net.HttpURLConnection
import java.net.URL

class MainActivity : AppCompatActivity() {

    private lateinit var webView: WebView
    private lateinit var loadingSpinner: ProgressBar
    private lateinit var loadingText: TextView
    private lateinit var errorLayout: View
    private lateinit var errorText: TextView

    private val goManager by lazy { GoManager(this) }
    private val scope = CoroutineScope(Dispatchers.Main + SupervisorJob())

    private var filePathCallback: ValueCallback<Array<Uri>>? = null

    private val fileChooserLauncher = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { result ->
        if (result.resultCode == Activity.RESULT_OK) {
            filePathCallback?.onReceiveValue(
                result.data?.data?.let { arrayOf(it) }
            )
        } else {
            filePathCallback?.onReceiveValue(null)
        }
        filePathCallback = null
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        try {
            setContentView(R.layout.activity_main)

            webView = findViewById(R.id.webView)
            loadingSpinner = findViewById(R.id.loadingSpinner)
            loadingText = findViewById(R.id.loadingText)
            errorLayout = findViewById(R.id.errorLayout)
            errorText = findViewById(R.id.errorText)
            val retryButton = findViewById<Button>(R.id.retryButton)

            retryButton.setOnClickListener { startApp() }

            // Log device info for debugging
            Log.i("iSwitch", "Device: ${Build.MANUFACTURER} ${Build.MODEL}, API ${Build.VERSION.SDK_INT}, ABIs: ${Build.SUPPORTED_ABIS.joinToString()}")

            startApp()
        } catch (e: Exception) {
            Log.e("iSwitch", "onCreate crashed", e)
            showError("Init error: ${e.message}")
        }
    }

    override fun onDestroy() {
        super.onDestroy()
        scope.cancel()
        goManager.stop()
    }

    override fun onBackPressed() {
        if (webView.canGoBack()) webView.goBack() else super.onBackPressed()
    }

    private fun startApp() {
        showLoading()
        scope.launch {
            try {
                val ready = withContext(Dispatchers.IO) { goManager.start() }
                if (ready) {
                    initWebView()
                } else {
                    showError("Failed to start P2P engine. Check logs.")
                }
            } catch (e: Exception) {
                Log.e("iSwitch", "startApp crashed", e)
                showError("Error: ${e.message}")
            }
        }
    }

    private fun showLoading() {
        loadingSpinner.visibility = View.VISIBLE
        loadingText.visibility = View.VISIBLE
        errorLayout.visibility = View.GONE
    }

    private fun showError(msg: String) {
        loadingSpinner.visibility = View.GONE
        loadingText.visibility = View.GONE
        errorLayout.visibility = View.VISIBLE
        errorText.text = msg
    }

    private fun initWebView() {
        loadingSpinner.visibility = View.GONE
        loadingText.visibility = View.GONE

        webView.apply {
            settings.apply {
                javaScriptEnabled = true
                domStorageEnabled = true
                allowFileAccess = true
                allowContentAccess = true
                setSupportMultipleWindows(false)
                loadWithOverviewMode = false
                useWideViewPort = false
                builtInZoomControls = false
                displayZoomControls = false
                cacheMode = WebSettings.LOAD_NO_CACHE
                userAgentString = settings.userAgentString + " iSwitch-Android"
                mixedContentMode = WebSettings.MIXED_CONTENT_ALWAYS_ALLOW
            }

            webChromeClient = object : WebChromeClient() {
                override fun onShowFileChooser(
                    webView: WebView?,
                    callback: ValueCallback<Array<Uri>>?,
                    params: FileChooserParams?
                ): Boolean {
                    filePathCallback = callback
                    val intent = Intent(Intent.ACTION_GET_CONTENT).apply {
                        addCategory(Intent.CATEGORY_OPENABLE)
                        type = "*/*"
                    }
                    fileChooserLauncher.launch(
                        Intent.createChooser(intent, "Select file")
                    )
                    return true
                }
            }

            webViewClient = object : WebViewClient() {
                override fun onPageFinished(view: WebView?, url: String?) {
                    super.onPageFinished(view, url)
                    view?.evaluateJavascript("""
                        (function() {
                            var baseUrl = '${goManager.getWebUrl().replace("'", "\\'")}';
                            window.androidDownload = function(id) {
                                Android.downloadFile(id);
                            };
                            document.addEventListener('click', function(e) {
                                var a = e.target.closest('a');
                                if (a && a.href && a.href.indexOf('/api/download/') > -1) {
                                    e.preventDefault();
                                    var id = a.href.split('/').pop();
                                    Android.downloadFile(id);
                                }
                            });
                        })();
                    """.trimIndent(), null)
                }

                override fun onReceivedError(view: WebView?, request: WebResourceRequest?, error: WebResourceError?) {
                    super.onReceivedError(view, request, error)
                    Log.e("iSwitch", "WebView error: ${error?.description} (code ${error?.errorCode})")
                }
            }

            setDownloadListener { url, _, contentDisposition, _, _ ->
                downloadFile(url, contentDisposition)
            }

            addJavascriptInterface(object {
                @android.webkit.JavascriptInterface
                fun downloadFile(transferId: String) {
                    scope.launch {
                        downloadFile("${goManager.getWebUrl()}/api/download/$transferId", null)
                    }
                }

                @android.webkit.JavascriptInterface
                fun getLocalInterfacesJson(): String {
                    val list = mutableListOf<Map<String, String>>()
                    try {
                        val en = java.net.NetworkInterface.getNetworkInterfaces()
                        while (en != null && en.hasMoreElements()) {
                            val intf = en.nextElement()
                            if (intf.isLoopback || !intf.isUp) continue
                            val enumIpAddr = intf.inetAddresses
                            while (enumIpAddr != null && enumIpAddr.hasMoreElements()) {
                                val inetAddress = enumIpAddr.nextElement()
                                if (!inetAddress.isLoopbackAddress && inetAddress is java.net.Inet4Address) {
                                    val ip = inetAddress.hostAddress ?: continue
                                    val parts = ip.split(".")
                                    if (parts.size == 4) {
                                        val segment = "${parts[0]}.${parts[1]}.${parts[2]}.0/24"
                                        val broadcast = "${parts[0]}.${parts[1]}.${parts[2]}.255"
                                        list.add(mapOf(
                                            "name" to intf.name,
                                            "ip" to ip,
                                            "mask" to "255.255.255.0",
                                            "segment" to segment,
                                            "broadcast" to broadcast
                                        ))
                                    }
                                }
                            }
                        }
                    } catch (e: Exception) {
                        Log.e("iSwitch", "getLocalInterfacesJson error", e)
                    }

                    val jsonBuilder = java.lang.StringBuilder("[")
                    list.forEachIndexed { index, map ->
                        jsonBuilder.append("{")
                        jsonBuilder.append("\"name\":\"${map["name"]}\",")
                        jsonBuilder.append("\"ip\":\"${map["ip"]}\",")
                        jsonBuilder.append("\"mask\":\"${map["mask"]}\",")
                        jsonBuilder.append("\"segment\":\"${map["segment"]}\",")
                        jsonBuilder.append("\"broadcast\":\"${map["broadcast"]}\"")
                        jsonBuilder.append("}")
                        if (index < list.size - 1) jsonBuilder.append(",")
                    }
                    jsonBuilder.append("]")
                    return jsonBuilder.toString()
                }
            }, "Android")

            loadUrl(goManager.getWebUrl())
        }
    }

    private fun downloadFile(urlStr: String, contentDisposition: String?) {
        Toast.makeText(this, getString(R.string.saving_file), Toast.LENGTH_SHORT).show()
        scope.launch(Dispatchers.IO) {
            try {
                val url = URL(urlStr)
                val conn = url.openConnection() as HttpURLConnection
                conn.connect()

                val fileName = parseFileName(contentDisposition, urlStr)
                val size = conn.contentLengthLong
                val inputStream = conn.inputStream

                saveFile(fileName, size, inputStream)
                conn.disconnect()

                withContext(Dispatchers.Main) {
                    Toast.makeText(this@MainActivity, "${getString(R.string.file_saved)}: $fileName", Toast.LENGTH_LONG).show()
                }
            } catch (e: Exception) {
                withContext(Dispatchers.Main) {
                    Toast.makeText(this@MainActivity, "${getString(R.string.save_failed)}: ${e.message}", Toast.LENGTH_LONG).show()
                }
            }
        }
    }

    private fun saveFile(fileName: String, size: Long, inputStream: java.io.InputStream) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            val values = ContentValues().apply {
                put(MediaStore.Downloads.DISPLAY_NAME, fileName)
                put(MediaStore.Downloads.MIME_TYPE, getMimeType(fileName))
                put(MediaStore.Downloads.SIZE, size)
                put(MediaStore.Downloads.IS_PENDING, 1)
            }

            val resolver = contentResolver
            val uri = resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
                ?: throw Exception("MediaStore insert failed")

            resolver.openOutputStream(uri)?.use { output ->
                inputStream.copyTo(output)
            }

            values.clear()
            values.put(MediaStore.Downloads.IS_PENDING, 0)
            resolver.update(uri, values, null, null)

            val intent = Intent(Intent.ACTION_VIEW).apply {
                setDataAndType(uri, getMimeType(fileName))
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            }
            startActivity(Intent.createChooser(intent, getString(R.string.share)))
        } else {
            val dir = Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS)
            val iSwitchDir = File(dir, "iSwitch").apply { mkdirs() }
            val file = File(iSwitchDir, fileName)

            file.outputStream().use { output ->
                inputStream.copyTo(output)
            }

            val uri = FileProvider.getUriForFile(this, "$packageName.fileprovider", file)
            val openIntent = Intent(Intent.ACTION_VIEW).apply {
                setDataAndType(uri, getMimeType(fileName))
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            }
            startActivity(Intent.createChooser(openIntent, getString(R.string.share)))
        }
    }

    private fun parseFileName(contentDisposition: String?, urlStr: String): String {
        contentDisposition?.let { cd ->
            val pattern = """filename[^;=\n]*=((['"]).*?\2|[^;\n]*)""".toRegex()
            pattern.find(cd)?.let {
                return it.groupValues[1].trim('"', '\'')
            }
        }
        return urlStr.substringAfterLast('/').ifEmpty { "download" }
    }

    private fun getMimeType(fileName: String): String {
        val mime = MimeTypeMap.getSingleton()
        val ext = fileName.substringAfterLast('.', "")
        return mime.getMimeTypeFromExtension(ext.lowercase()) ?: "application/octet-stream"
    }
}
