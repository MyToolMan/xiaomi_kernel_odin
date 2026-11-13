package com.miuitoolman.voltforge.burnguard;

import android.app.Activity;
import android.content.Intent;
import android.graphics.Color;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.provider.Settings;
import android.view.Gravity;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.TextView;

public final class MainActivity extends Activity {
    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        boolean autoStart = getIntent().getBooleanExtra("autostart", false);
        getWindow().setStatusBarColor(Color.BLACK);

        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setGravity(Gravity.CENTER_HORIZONTAL);
        root.setPadding(dp(28), dp(56), dp(28), dp(28));
        root.setBackgroundColor(Color.rgb(8, 11, 16));

        TextView title = new TextView(this);
        title.setText("VOLT FORGE\nBURN GUARD");
        title.setTextColor(Color.rgb(220, 240, 255));
        title.setTextSize(30);
        title.setGravity(Gravity.CENTER);
        root.addView(title, matchWrap());

        TextView body = new TextView(this);
        body.setText("Static black mask over the top display strip.\n"
                + "No animation, polling, rendering loop, or network access.");
        body.setTextColor(Color.rgb(138, 160, 180));
        body.setTextSize(16);
        body.setGravity(Gravity.CENTER);
        body.setPadding(0, dp(28), 0, dp(36));
        root.addView(body, matchWrap());

        Button start = new Button(this);
        start.setText("ENABLE / REFRESH MASK");
        start.setAllCaps(false);
        start.setOnClickListener(v -> startMask());
        root.addView(start, matchWrap());

        Button stop = new Button(this);
        stop.setText("DISABLE MASK");
        stop.setAllCaps(false);
        stop.setOnClickListener(v -> {
            stopService(new Intent(this, BurnGuardService.class));
            finish();
        });
        root.addView(stop, matchWrap());

        setContentView(root);

        if (!Settings.canDrawOverlays(this)) {
            if (autoStart) {
                finish();
                return;
            }
            startActivity(new Intent(
                    Settings.ACTION_MANAGE_OVERLAY_PERMISSION,
                    Uri.parse("package:" + getPackageName())));
            return;
        }
        startMask();
        if (autoStart) {
            finish();
        }
    }

    private void startMask() {
        Intent service = new Intent(this, BurnGuardService.class);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(service);
        } else {
            startService(service);
        }
    }

    private int dp(int value) {
        return Math.round(value * getResources().getDisplayMetrics().density);
    }

    private LinearLayout.LayoutParams matchWrap() {
        return new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT);
    }
}
