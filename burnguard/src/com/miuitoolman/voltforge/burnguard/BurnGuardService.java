package com.miuitoolman.voltforge.burnguard;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.res.Configuration;
import android.graphics.Color;
import android.graphics.Insets;
import android.graphics.PixelFormat;
import android.os.IBinder;
import android.view.Gravity;
import android.view.View;
import android.view.WindowInsets;
import android.view.WindowManager;
import android.view.WindowMetrics;

/**
 * A deliberately static opaque mask. It has no render loop, animation,
 * telemetry polling, or periodic timer.
 */
public final class BurnGuardService extends Service {
    private static final String CHANNEL_ID = "burn_guard";
    private static final int NOTIFICATION_ID = 0x0D1B;

    private WindowManager windowManager;
    private View mask;
    private WindowManager.LayoutParams params;

    @Override
    public void onCreate() {
        super.onCreate();
        createNotificationChannel();
        startForeground(NOTIFICATION_ID, createNotification());
        windowManager = (WindowManager) getSystemService(Context.WINDOW_SERVICE);
        showMask();
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        showMask();
        return START_STICKY;
    }

    @Override
    public void onConfigurationChanged(Configuration newConfig) {
        super.onConfigurationChanged(newConfig);
        resizeMask();
    }

    @Override
    public void onDestroy() {
        hideMask();
        super.onDestroy();
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }

    private int statusBarMaskHeight() {
        WindowMetrics metrics = windowManager.getCurrentWindowMetrics();
        Insets insets = metrics.getWindowInsets().getInsetsIgnoringVisibility(
                WindowInsets.Type.statusBars() | WindowInsets.Type.displayCutout());
        return Math.max(insets.top, 1);
    }

    private void showMask() {
        if (windowManager == null || mask != null) {
            return;
        }

        mask = new View(this);
        mask.setBackgroundColor(Color.BLACK);
        mask.setImportantForAccessibility(View.IMPORTANT_FOR_ACCESSIBILITY_NO);

        params = new WindowManager.LayoutParams(
                WindowManager.LayoutParams.MATCH_PARENT,
                statusBarMaskHeight(),
                WindowManager.LayoutParams.TYPE_APPLICATION_OVERLAY,
                WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE
                        | WindowManager.LayoutParams.FLAG_NOT_TOUCHABLE
                        | WindowManager.LayoutParams.FLAG_NOT_TOUCH_MODAL
                        | WindowManager.LayoutParams.FLAG_LAYOUT_IN_SCREEN
                        | WindowManager.LayoutParams.FLAG_LAYOUT_NO_LIMITS
                        | WindowManager.LayoutParams.FLAG_HARDWARE_ACCELERATED,
                PixelFormat.OPAQUE);
        params.gravity = Gravity.TOP | Gravity.START;
        params.x = 0;
        params.y = 0;
        params.setTitle("VOLT FORGE Burn Guard");

        windowManager.addView(mask, params);
    }

    private void resizeMask() {
        if (windowManager == null || mask == null || params == null) {
            return;
        }
        params.height = statusBarMaskHeight();
        params.width = WindowManager.LayoutParams.MATCH_PARENT;
        params.x = 0;
        params.y = 0;
        windowManager.updateViewLayout(mask, params);
    }

    private void hideMask() {
        if (windowManager != null && mask != null) {
            try {
                windowManager.removeView(mask);
            } catch (IllegalArgumentException ignored) {
                // The system may already have detached the window during shutdown.
            }
        }
        mask = null;
        params = null;
    }

    private void createNotificationChannel() {
        NotificationChannel channel = new NotificationChannel(
                CHANNEL_ID,
                getString(R.string.notification_channel),
                NotificationManager.IMPORTANCE_LOW);
        channel.setDescription(getString(R.string.notification_text));
        channel.setShowBadge(false);
        getSystemService(NotificationManager.class).createNotificationChannel(channel);
    }

    private Notification createNotification() {
        return new Notification.Builder(this, CHANNEL_ID)
                .setSmallIcon(R.drawable.ic_burn_guard)
                .setContentTitle(getString(R.string.notification_title))
                .setContentText(getString(R.string.notification_text))
                .setOngoing(true)
                .setShowWhen(false)
                .setCategory(Notification.CATEGORY_SERVICE)
                .build();
    }
}
