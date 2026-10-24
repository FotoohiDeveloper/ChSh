package ir.chador.app.ui

import android.Manifest
import android.content.pm.PackageManager
import android.media.AudioManager
import android.media.ToneGenerator
import android.view.HapticFeedbackConstants
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.camera.core.Camera
import androidx.camera.core.CameraSelector
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.ImageProxy
import androidx.camera.core.Preview
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.camera.view.PreviewView
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.content.ContextCompat
import androidx.lifecycle.compose.LocalLifecycleOwner
import com.google.mlkit.vision.barcode.BarcodeScannerOptions
import com.google.mlkit.vision.barcode.BarcodeScanning
import com.google.mlkit.vision.barcode.common.Barcode
import com.google.mlkit.vision.common.InputImage
import ir.chador.core.ScanResult
import java.util.concurrent.Executors

/** Sound + vibration after each scan, so the operator can work without looking at the screen. */
class ScanFeedback(private val view: android.view.View) {
    private val tone: ToneGenerator? = try { ToneGenerator(AudioManager.STREAM_NOTIFICATION, 80) } catch (e: Exception) { null }

    fun play(result: ScanResult) {
        val ok = result == ScanResult.Added
        try {
            tone?.startTone(if (ok) ToneGenerator.TONE_PROP_BEEP else ToneGenerator.TONE_PROP_NACK, if (ok) 80 else 250)
        } catch (e: Exception) { /* silent device */ }
        view.performHapticFeedback(if (ok) HapticFeedbackConstants.KEYBOARD_TAP else HapticFeedbackConstants.LONG_PRESS)
    }

    fun release() { tone?.release() }
}

@Composable
fun rememberScanFeedback(): ScanFeedback {
    val view = LocalView.current
    val fb = remember(view) { ScanFeedback(view) }
    DisposableEffect(fb) { onDispose { fb.release() } }
    return fb
}

/**
 * Camera scanner (QR / Data Matrix / Code 128) with a manual code field underneath for damaged labels.
 * [onCode] gets the raw text; the screen's presenter validates it. Repeats of the same code within
 * 1.5 s are ignored so one label is not counted twice while the camera is still pointing at it.
 */
@Composable
fun ScannerPanel(onCode: (String) -> Unit, modifier: Modifier = Modifier, cameraHeight: Int = 220) {
    val ctx = LocalContext.current
    var granted by remember { mutableStateOf(ContextCompat.checkSelfPermission(ctx, Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) }
    var asked by remember { mutableStateOf(false) }
    val launcher = rememberPermissionLauncher { granted = it; asked = true }
    var manual by remember { mutableStateOf("") }
    val current by rememberUpdatedState(onCode)

    Column(modifier, verticalArrangement = Arrangement.spacedBy(8.dp)) {
        if (granted) {
            CameraPreview(onCode = { current(it) }, height = cameraHeight)
        } else {
            AppCard {
                Text("برای اسکن برچسب چادر، دسترسی دوربین لازم است.")
                if (asked) Text("دسترسی داده نشد. می‌توانید از تنظیمات گوشی آن را فعال کنید یا کد را دستی وارد کنید.", style = MaterialTheme.typography.bodySmall)
                PrimaryButton("اجازهٔ دوربین", onClick = { launcher.launch(Manifest.permission.CAMERA) })
            }
        }
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
            Field(manual, { manual = it }, "کد چادر (دستی)", modifier = Modifier.weight(1f), keyboard = KeyboardType.Text, placeholder = "CH-000123", ltr = true)
            OutlinedButton(onClick = { if (manual.isNotBlank()) { current(manual.trim()); manual = "" } }, modifier = Modifier.height(56.dp)) { Text("افزودن") }
        }
    }
}

@Composable
private fun rememberPermissionLauncher(onResult: (Boolean) -> Unit) =
    rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission(), onResult)

@Composable
private fun CameraPreview(onCode: (String) -> Unit, height: Int) {
    val ctx = LocalContext.current
    val owner = LocalLifecycleOwner.current
    val executor = remember { Executors.newSingleThreadExecutor() }
    val scanner = remember {
        BarcodeScanning.getClient(
            BarcodeScannerOptions.Builder()
                .setBarcodeFormats(Barcode.FORMAT_QR_CODE, Barcode.FORMAT_DATA_MATRIX, Barcode.FORMAT_CODE_128)
                .build(),
        )
    }
    var camera by remember { mutableStateOf<Camera?>(null) }
    var torch by remember { mutableStateOf(false) }
    val last = remember { arrayOf("" , 0L) }
    val callback by rememberUpdatedState(onCode)
    val mainExecutor = remember { ContextCompat.getMainExecutor(ctx) }

    DisposableEffect(Unit) {
        onDispose {
            executor.shutdown()
            scanner.close()
        }
    }

    Box(Modifier.fillMaxWidth().height(height.dp).clip(RoundedCornerShape(16.dp))) {
        AndroidView(
            modifier = Modifier.fillMaxWidth().height(height.dp),
            factory = { c ->
                val preview = PreviewView(c).apply { scaleType = PreviewView.ScaleType.FILL_CENTER }
                val providerFuture = ProcessCameraProvider.getInstance(c)
                providerFuture.addListener({
                    val provider = providerFuture.get()
                    val previewUse = Preview.Builder().build().also { it.setSurfaceProvider(preview.surfaceProvider) }
                    val analysis = ImageAnalysis.Builder()
                        .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
                        .build()
                    analysis.setAnalyzer(executor) { proxy -> analyze(proxy, scanner) { raw ->
                        val now = System.currentTimeMillis()
                        if (raw != last[0] || now - (last[1] as Long) > 1500) {
                            last[0] = raw; last[1] = now
                            mainExecutor.execute { callback(raw) }
                        }
                    } }
                    try {
                        provider.unbindAll()
                        camera = provider.bindToLifecycle(owner, CameraSelector.DEFAULT_BACK_CAMERA, previewUse, analysis)
                    } catch (e: Exception) {
                        // camera busy or unavailable: the manual field below still works
                    }
                }, mainExecutor)
                preview
            },
        )
        val cam = camera
        if (cam != null && cam.cameraInfo.hasFlashUnit()) {
            TextButton(
                onClick = { torch = !torch; cam.cameraControl.enableTorch(torch) },
                modifier = Modifier.align(Alignment.TopStart).padding(4.dp),
            ) { Text(if (torch) "🔦 خاموش" else "🔦 چراغ", color = androidx.compose.ui.graphics.Color.White) }
        }
    }
    LaunchedEffect(camera) { /* keeps the torch state in sync after rebinding */ torch = false }
}

@Suppress("UnsafeOptInUsageError")
private fun analyze(proxy: ImageProxy, scanner: com.google.mlkit.vision.barcode.BarcodeScanner, onRaw: (String) -> Unit) {
    val media = proxy.image
    if (media == null) { proxy.close(); return }
    val input = InputImage.fromMediaImage(media, proxy.imageInfo.rotationDegrees)
    scanner.process(input)
        .addOnSuccessListener { list -> list.firstNotNullOfOrNull { it.rawValue }?.let(onRaw) }
        .addOnCompleteListener { proxy.close() }
}
