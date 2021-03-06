/*
Copyright 2011 Google Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package com.danga.camli;

import java.io.BufferedInputStream;
import java.io.FileDescriptor;
import java.io.FileInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;

import android.os.AsyncTask;
import android.util.Log;

public class Util {
    private static final String TAG = "Camli_Util";

    public static String slurp(InputStream in) throws IOException {
        StringBuilder sb = new StringBuilder();
        byte[] b = new byte[4096];
        for (int n; (n = in.read(b)) != -1;) {
            sb.append(new String(b, 0, n));
        }
        return sb.toString();
    }

    public static void runAsync(final Runnable r) {
        new AsyncTask<Void, Void, Void>() {
            @Override
            protected Void doInBackground(Void... unused) {
                r.run();
                return null;
            }
        }.execute();
    }

    private static final String HEX = "0123456789abcdef";

    private static String getHex(byte[] raw) {
        if (raw == null) {
            return null;
        }
        final StringBuilder hex = new StringBuilder(2 * raw.length);
        for (final byte b : raw) {
            hex.append(HEX.charAt((b & 0xF0) >> 4)).append(
                    HEX.charAt((b & 0x0F)));
        }
        return hex.toString();
    }

    // Requires that the fd be seeked to the beginning.
    public static String getSha1(FileDescriptor fd) {
        MessageDigest md;
        try {
            md = MessageDigest.getInstance("SHA-1");
        } catch (NoSuchAlgorithmException e) {
            throw new RuntimeException(e);
        }
        byte[] b = new byte[4096];
        FileInputStream fis = new FileInputStream(fd);
        InputStream is = new BufferedInputStream(fis, 4096);
        try {
            for (int n; (n = is.read(b)) != -1;) {
                md.update(b, 0, n);
            }
        } catch (IOException e) {
            Log.w(TAG, "IOException while computing SHA-1");
            return null;
        }
        byte[] sha1hash = new byte[40];
        sha1hash = md.digest();
        return getHex(sha1hash);
    }
}
