/*
 * JS emulator library
 * 
 * Copyright (c) 2017 Fabrice Bellard
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL
 * THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
 * THE SOFTWARE.
 */
mergeInto(LibraryManager.library, {
    /* wash patch: batch console_write at the WASM↔JS boundary.
     * HTIF (riscv_machine.c:htif_handle_cmd) emits exactly 1 byte per
     * call, so the unpatched form did String.fromCharCode + term.write
     * per character — 3000+ JS↔C round-trips per cold boot, each
     * crossing the boundary, allocating a 1-char string, and running
     * Bellard's Term VT100 state machine for one glyph.
     *
     * Strategy: copy bytes into a JS-side Uint8Array; on the first
     * write of a batch, schedule a microtask via Promise.resolve()
     * to flush all accumulated bytes as one term.write call.
     * Microtasks drain when the current synchronous JS task
     * (virt_machine_run → virt_machine_interp) returns to the event
     * loop, so all chars produced inside one interp iteration
     * coalesce into a single term.write. */
    console_write: function(opaque, buf, len)
    {
        if (typeof window === 'undefined') {
            /* node/test fallback: pass-through to whatever term is. */
            var s = String.fromCharCode.apply(String, HEAPU8.subarray(buf, buf + len));
            if (typeof term !== 'undefined' && term) term.write(s);
            return;
        }
        var W = window.__washHtif;
        if (!W) {
            W = window.__washHtif = { buf: new Uint8Array(8192), len: 0, pending: false };
        }
        var off = W.len;
        if (off + len > W.buf.length) {
            var newSize = W.buf.length;
            while (newSize < off + len) newSize <<= 1;
            var nb = new Uint8Array(newSize);
            nb.set(W.buf.subarray(0, off));
            W.buf = nb;
        }
        W.buf.set(HEAPU8.subarray(buf, buf + len), off);
        W.len = off + len;
        if (!W.pending) {
            W.pending = true;
            Promise.resolve().then(function () {
                W.pending = false;
                var n = W.len;
                if (n === 0) return;
                W.len = 0;
                /* String.fromCharCode.apply has a max-args limit (~64K
                 * on most engines, lower on Safari). Chunk to stay
                 * safe even for the rare burst that exceeds it. */
                var s = '';
                for (var i = 0; i < n; i += 16384) {
                    var end = (i + 16384 < n) ? i + 16384 : n;
                    s += String.fromCharCode.apply(String, W.buf.subarray(i, end));
                }
                if (typeof term !== 'undefined' && term && typeof term.write === 'function') {
                    term.write(s);
                }
            });
        }
    },

    /* wash patch: multi-channel sink for the per-wash virtio-console
       devices. opaque carries the channel index (set by
       virtio_console_dev_init in jsemu.c). Dispatches to
       window.washConsoles[ch].onOutput. Missing handler → bytes
       dropped (no-op). */
    virtio_console_out_write: function(opaque, buf, len)
    {
        if (typeof window === 'undefined') return;
        var ch = opaque | 0;
        var arr = window.washConsoles;
        if (!arr || !arr[ch] || typeof arr[ch].onOutput !== 'function') return;
        /* Copy out; HEAPU8 view may be invalidated when WASM grows. */
        var bytes = new Uint8Array(HEAPU8.subarray(buf, buf + len));
        try { arr[ch].onOutput(bytes); } catch (e) { console.error('[washConsoles['+ch+']]', e); }
    },

    /* wash multiport: per-port sink. window.washVports[port].onOutput
       receives bytes from /dev/vport0p{port}. */
    virtio_vport_out_write: function(opaque, buf, len)
    {
        if (typeof window === 'undefined') return;
        var port = opaque | 0;
        var arr = window.washVports;
        if (!arr || !arr[port] || typeof arr[port].onOutput !== 'function') return;
        var bytes = new Uint8Array(HEAPU8.subarray(buf, buf + len));
        try { arr[port].onOutput(bytes); } catch (e) { console.error('[washVports['+port+']]', e); }
    },

    console_get_size: function(pw, ph)
    {
        var w, h, r;
        r = term.getSize();
        HEAPU32[pw >> 2] = r[0];
        HEAPU32[ph >> 2] = r[1];
    },

    fs_export_file: function(filename, buf, buf_len)
    {
        var _filename = Pointer_stringify(filename);
//        console.log("exporting " + _filename);
        var data = HEAPU8.subarray(buf, buf + buf_len);
        var file = new Blob([data], { type: "application/octet-stream" });
        var url = URL.createObjectURL(file);
        var a = document.createElement("a");
        a.href = url;
        a.setAttribute("download", _filename);
        a.innerHTML = "downloading";
        document.body.appendChild(a);
        /* click on the link and remove it */
        setTimeout(function() {
            a.click();
            document.body.removeChild(a);
        }, 50);
    },

    emscripten_async_wget3_data: function(url, request, user, password, post_data, post_data_len, arg, free, onload, onerror, onprogress) {
    var _url = Pointer_stringify(url);
    var _request = Pointer_stringify(request);
    var _user;
    var _password;

      var http = new XMLHttpRequest();

      if (user)
          _user = Pointer_stringify(user);
      else
          _user = null;
      if (password)
          _password = Pointer_stringify(password);
      else
          _password = null;
        
      http.open(_request, _url, true);
      http.responseType = 'arraybuffer';
      if (_user) {
          http.setRequestHeader("Authorization", "Basic " + btoa(_user + ':' + _password));
      }
        
    var handle = Browser.getNextWgetRequestHandle();

    // LOAD
    http.onload = function http_onload(e) {
      if (http.status == 200 || _url.substr(0,4).toLowerCase() != "http") {
        var byteArray = new Uint8Array(http.response);
        var buffer = _malloc(byteArray.length);
        HEAPU8.set(byteArray, buffer);
        if (onload) Runtime.dynCall('viiii', onload, [handle, arg, buffer, byteArray.length]);
        if (free) _free(buffer);
      } else {
        if (onerror) Runtime.dynCall('viiii', onerror, [handle, arg, http.status, http.statusText]);
      }
      delete Browser.wgetRequests[handle];
    };

    // ERROR
    http.onerror = function http_onerror(e) {
      if (onerror) {
        Runtime.dynCall('viiii', onerror, [handle, arg, http.status, http.statusText]);
      }
      delete Browser.wgetRequests[handle];
    };

    // PROGRESS
    http.onprogress = function http_onprogress(e) {
      if (onprogress) Runtime.dynCall('viiii', onprogress, [handle, arg, e.loaded, e.lengthComputable || e.lengthComputable === undefined ? e.total : 0]);
    };

    // ABORT
    http.onabort = function http_onabort(e) {
      delete Browser.wgetRequests[handle];
    };

    // Useful because the browser can limit the number of redirection
    try {
      if (http.channel instanceof Ci.nsIHttpChannel)
      http.channel.redirectionLimit = 0;
    } catch (ex) { /* whatever */ }

    if (_request == "POST") {
      var _post_data = HEAPU8.subarray(post_data, post_data + post_data_len);
        //Send the proper header information along with the request
      http.setRequestHeader("Content-type", "application/octet-stream");
      http.setRequestHeader("Content-length", post_data_len);
      http.setRequestHeader("Connection", "close");
      http.send(_post_data);
    } else {
      http.send(null);
    }

    Browser.wgetRequests[handle] = http;

    return handle;
  },

  fs_wget_update_downloading: function (flag)
  {
      update_downloading(Boolean(flag));
  },
    
  fb_refresh: function(opaque, data, x, y, w, h, stride)
  {
      var i, j, v, src, image_data, dst_pos, display, dst_pos1, image_stride;

      display = graphic_display;
      /* current x = 0 and w = width for all refreshes */
//      console.log("fb_refresh: x=" + x + " y=" + y + " w=" + w + " h=" + h);
      image_data = display.image.data;
      image_stride = display.width * 4;
      dst_pos1 = (y * display.width + x) * 4;
      for(i = 0; i < h; i = (i + 1) | 0) {
          src = data;
          dst_pos = dst_pos1;
          for(j = 0; j < w; j = (j + 1) | 0) {
              v = HEAPU32[src >> 2];
              image_data[dst_pos] = (v >> 16) & 0xff;
              image_data[dst_pos + 1] = (v >> 8) & 0xff;
              image_data[dst_pos + 2] = v & 0xff;
              image_data[dst_pos + 3] = 0xff; /* XXX: do it once */
              src = (src + 4) | 0;
              dst_pos = (dst_pos + 4) | 0;
          }
          data = (data + stride) | 0;
          dst_pos1 = (dst_pos1 + image_stride) | 0;
      }
      display.ctx.putImageData(display.image, 0, 0, x, y, w, h);
  },

  net_recv_packet: function(bs, buf, buf_len)
  {
      if (net_state) {
          net_state.recv_packet(HEAPU8.subarray(buf, buf + buf_len));
      }
  },

  /* file buffer API */
  file_buffer_get_new_handle: function()
  {
      var h;
      if (typeof Browser.fbuf_table == "undefined") {
          Browser.fbuf_table = new Object();
          Browser.fbuf_next_handle = 1;
      }
      for(;;) {
          h = Browser.fbuf_next_handle;
          Browser.fbuf_next_handle++;
          if (Browser.fbuf_next_handle == 0x80000000)
              Browser.fbuf_next_handle = 1;
          if (typeof Browser.fbuf_table[h] == "undefined") {
//              console.log("new handle=" + h);
              return h;
          }
      }
  },
    
  file_buffer_init: function(bs)
  {
      var h;
      HEAPU32[bs >> 2] = 0;
      HEAPU32[(bs + 4) >> 2] = 0;
  },

  file_buffer_resize__deps: ['file_buffer_get_new_handle'],
  file_buffer_resize: function(bs, new_size)
  {
      var h, size, new_data, size1, i, data;
      h = HEAPU32[bs >> 2];
      size = HEAPU32[(bs + 4) >> 2];
      if (new_size == 0) {
          if (h != 0) {
              delete Browser.fbuf_table[h];
              h = 0;
          }
      } else if (size == 0) {
          h = _file_buffer_get_new_handle();
          new_data = new Uint8Array(new_size);
          Browser.fbuf_table[h] = new_data;
      } else if (size != new_size) {
          data = Browser.fbuf_table[h];
          new_data = new Uint8Array(new_size);
          if (new_size > size) {
              new_data.set(data, 0);
          } else {
              for(i = 0; i < new_size; i = (i + 1) | 0)
                  new_data[i] = data[i];
          }
          Browser.fbuf_table[h] = new_data;
      }
      HEAPU32[bs >> 2] = h;
      HEAPU32[(bs + 4) >> 2] = new_size;
      return 0;
  },
  
  file_buffer_reset: function(bs)
  {
      _file_buffer_resize(bs, 0);
      _file_buffer_init(bs);
  },
    
  file_buffer_write: function(bs, offset, buf, size)
  {
      var h, data, i;
      h = HEAPU32[bs >> 2];
      if (h) {
          data = Browser.fbuf_table[h];
          for(i = 0; i < size; i = (i + 1) | 0) {
              data[offset + i] = HEAPU8[buf + i];
          }
      }
  },
    
  file_buffer_read: function(bs, offset, buf, size)
  {
      var h, data, i;
      h = HEAPU32[bs >> 2];
      if (h) {
          data = Browser.fbuf_table[h];
          for(i = 0; i < size; i = (i + 1) | 0) {
              HEAPU8[buf + i] = data[offset + i];
          }
      }
  },

  file_buffer_set: function(bs, offset, val, size)
  {
      var h, data, i;
      h = HEAPU32[bs >> 2];
      if (h) {
          data = Browser.fbuf_table[h];
          for(i = 0; i < size; i = (i + 1) | 0) {
              data[offset + i] = val;
          }
      }
  },
   
});
