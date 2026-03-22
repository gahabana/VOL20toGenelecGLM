# Research: Novel Approaches for Detecting GLM Power Button State

**Date**: 2026-03-22
**Context**: GLM is a JUCE-based OpenGL application. Current approach uses `PIL.ImageGrab` + median RGB pixel sampling. Works ~99% of the time but returns "unknown" during startup when GLM renders the button incorrectly.

---

## Approach 1: Windows UI Automation (UIA / MSAA)

### Technical Feasibility: LOW

**The problem**: GLM is built with JUCE, which uses OpenGL for rendering — not standard Win32 controls. JUCE added accessibility support in v6.1, but it has a **known, confirmed limitation**: UI automation tools (FlaInspect, Accessibility Insights, Inspect.exe) can only see the **root window** — child UI elements (like the power button) are NOT exposed in the automation tree.

This was reported on the JUCE forum ([Windows UI Automation thread](https://forum.juce.com/t/windows-ui-automation/57719)) and confirmed as a limitation. The JUCE `AccessibilityHandler` wraps Components but the hierarchy is not fully surfaced to Windows UIA clients.

**Evidence**: pywinauto already finds the GLM window via `Desktop(backend="win32").windows(class_name_re=r"JUCE_.*")` — but this only gives us the top-level window, not internal buttons. Switching to `backend="uia"` would not help because JUCE doesn't expose the button as a UIA element.

**Python libraries**: pywinauto (already in use), comtypes (for raw UIA)

**Pros vs current approach**:
- Would be the "proper" way if it worked
- No pixel/color dependency

**Cons**:
- **Does not work** — JUCE doesn't expose internal controls to UIA
- Would require Genelec to implement proper JUCE accessibility handlers
- No workaround available from our side

**Confidence**: VERY LOW — this is a dead end unless Genelec updates GLM's accessibility.

---

## Approach 2: OpenCV Template Matching

### Technical Feasibility: HIGH

**How it works**: Instead of sampling a small pixel patch and classifying by RGB thresholds, capture a larger region around the button and match it against pre-captured template images of the "on" and "off" states using `cv2.matchTemplate()`.

**Key advantages over current pixel sampling**:
- Matches against an entire region (e.g., 40x40 pixels), not just a 9x9 patch
- Uses normalized cross-correlation — tolerant of minor brightness/color shifts
- Can use multiple templates (different button states, startup rendering variants)
- Confidence score (0.0-1.0) provides explicit "match quality" — better than heuristic RGB thresholds
- The `confidence` parameter approach is proven: PyAutoGUI's `locateOnScreen()` uses cv2 under the hood

**Implementation sketch**:
1. Capture reference templates: `button_on.png`, `button_off.png` (and optionally `button_startup.png`)
2. Grab the button region using existing `ImageGrab.grab(bbox=...)` or faster method
3. Run `cv2.matchTemplate(region, template, cv2.TM_CCOEFF_NORMED)`
4. Compare confidence scores — highest match above threshold wins

**Reliability**: Template matching on GUI elements is extremely reliable because:
- The button doesn't rotate, scale, or change perspective
- OpenCV's matching is computed channel-by-channel and summed
- Threshold of 0.8+ is standard for near-identical templates
- Can handle the startup rendering bug by having a "startup/loading" template

**Evidence**:
- [OpenCV Template Matching Tutorial](https://docs.opencv.org/4.x/de/da9/tutorial_template_matching.html) — official docs
- [CvMatch](https://github.com/Triscuit2311/CvMatch) — template matching for any window
- [Game automation with OpenCV](https://www.tautvidas.com/blog/2018/02/automating-basic-tasks-in-games-with-opencv-and-python/) — real-world GUI automation
- [PyAutoGUI + OpenCV integration](https://coderslegacy.com/python/opencv-with-pyautogui-image-recognition/) — established pattern
- PyAutoGUI issue #168 suggests rewriting locateOnScreen with OpenCV for better performance

**Python libraries**: `opencv-python` (cv2), `numpy`

**Pros vs current approach**:
- More robust — matches structural pattern, not just color values
- Explicit confidence score instead of heuristic thresholds
- Can detect "startup rendering" state as a third template
- Well-tested technique in game/GUI automation
- Small additional dependency (opencv-python)

**Cons**:
- Slightly more CPU per check (negligible — single template match on 40x40 region is sub-millisecond)
- Requires capturing reference template images from the actual GLM UI
- Templates may need updating if GLM UI changes (same as current color thresholds)
- Still depends on screen capture working (same as current approach)

**Confidence**: HIGH — this is the most practical upgrade path. Can be implemented alongside current pixel sampling as a verification layer.

---

## Approach 3: GPU/DirectX Screen Capture (Windows Graphics Capture / DXGI)

### Technical Feasibility: MEDIUM-HIGH

**Two APIs available**:

### 3a. DXGI Desktop Duplication API
- Captures the composited desktop at the GPU level
- **Works with windowed OpenGL apps** (confirmed by Microsoft docs)
- Does NOT work with full-screen exclusive mode (not relevant — GLM runs windowed)
- Captures at 240+ FPS — much faster than ImageGrab
- Not dependent on the graphics API that applications use

### 3b. Windows Graphics Capture API (Windows.Graphics.Capture)
- Modern API (Windows 10 1903+)
- Can target a **specific window by HWND** — captures even when window is obscured/behind other windows
- Leverages GPU compositing — may bypass rendering issues that affect ImageGrab

**Why this might solve the startup bug**: `ImageGrab.grab()` uses GDI (BitBlt) which may fail or return stale data during OpenGL context initialization. Both DXGI and Graphics Capture API work at the compositor level, which may capture the actual rendered output more reliably.

**Python libraries**:
- [`windows-capture`](https://pypi.org/project/windows-capture/) — Rust+Python, uses Graphics Capture API, pip-installable, **fastest Python capture library**
- [`windows-capture-interpreter`](https://pypi.org/project/windows-capture-interpreter/) — fork with **HWND support** for window-specific capture
- [`DXcam`](https://github.com/ra1nty/DXcam) — Desktop Duplication API, 240+ FPS, can capture specific regions
- [`BetterCam`](https://github.com/RootKit-Org/BetterCam) — DXcam fork, more maintained
- [`d3dshot`](https://pypi.org/project/d3dshot/) — pure Python Desktop Duplication API
- [`wincam`](https://github.com/lovettchris/wincam) — fast screen capture
- [`python-winsdk`](https://github.com/pywinrt/python-winsdk) — raw WinRT bindings for Graphics Capture

**Evidence**:
- [Microsoft Desktop Duplication API docs](https://learn.microsoft.com/en-us/windows/win32/direct3ddxgi/desktop-dup-api) — "not dependent on the graphics API that applications use"
- [Fast Window Capture](https://learncodebygaming.com/blog/fast-window-capture) — practical comparison of capture methods
- [OBS Forum: WGC vs DXGI DD](https://obsproject.com/forum/threads/windows-graphics-capture-vs-dxgi-desktop-duplication.149320/) — real-world comparison

**Pros vs current approach**:
- Captures at GPU compositor level — may bypass startup rendering issues
- Window-specific capture (WGC) works even when window is behind other windows
- Dramatically faster (240+ FPS vs ImageGrab's ~30 FPS)
- No GDI dependency — avoids BitBlt stale data issues

**Cons**:
- Additional dependency (Rust-based libraries need specific Python versions)
- Windows 10 1903+ required (should be fine for our VM)
- More complex setup than simple ImageGrab
- Still returns pixel data — needs color analysis or template matching on top
- May not actually solve the startup bug (the bug could be in GLM's rendering, not capture)

**Confidence**: MEDIUM-HIGH for better capture reliability. The startup bug may be in GLM's OpenGL rendering itself (not in our capture method), in which case this won't help.

---

## Approach 4: OpenGL Framebuffer Reading

### Technical Feasibility: LOW

**The theory**: Hook into GLM's OpenGL context and call `glReadPixels()` to read the framebuffer directly from GPU memory, bypassing screen capture entirely.

**The reality**:
- `glReadPixels()` can ONLY be called from the thread that owns the OpenGL context
- An external process CANNOT call glReadPixels on another process's context
- Requires DLL injection into GLM's process to run code in its OpenGL thread
- Tools like RenderDoc and apitrace do this — they inject DLLs that hook OpenGL calls
- [apiparse](https://github.com/aschrein/apiparse) — learning project based on RenderDoc/apitrace DLL hooking

**Implementation would require**:
1. Write a C/C++ DLL that hooks `wglSwapBuffers` or `glDrawArrays`
2. In the hook, call `glReadPixels` on the framebuffer
3. Share the pixel data via shared memory or named pipe
4. Inject the DLL into GLM's process using `CreateRemoteThread`
5. Read the shared data from Python

**Python libraries**: `ctypes` (for DLL injection), but the hook itself must be native C/C++

**Pros vs current approach**:
- Reads actual GPU framebuffer — the "ground truth" of what's rendered
- Not affected by window occlusion, compositor issues, etc.

**Cons**:
- Extremely complex to implement (DLL injection + OpenGL hooking)
- Fragile — any GLM update could break the hook
- May trigger antivirus/security software
- Requires maintaining a native C/C++ DLL
- Could crash GLM if the hook has bugs
- Way over-engineered for detecting a single button

**Confidence**: LOW — technically possible but absurdly over-engineered for this use case.

---

## Approach 5: Process Memory Reading

### Technical Feasibility: LOW-MEDIUM

**The theory**: The power button state must be stored somewhere in GLM's process memory. Find the memory address and read it directly using `ReadProcessMemory`.

**How it would work**:
1. Use Cheat Engine to scan GLM's memory while toggling power on/off
2. Find the boolean/enum address that changes between states
3. Use Python to read that address at runtime

**Python libraries**:
- [`PyMemoryEditor`](https://pypi.org/project/PyMemoryEditor/) — cross-platform, read/write/search process memory
- `ctypes` with `kernel32.ReadProcessMemory` — direct Win32 API
- [`WinAppDbg`](https://winappdbg.readthedocs.io/) — process instrumentation and memory manipulation

**Evidence**:
- [Game Hacking with Python + Cheat Engine](https://noob3xploiter.medium.com/game-hacking-with-python-and-cheat-engine-5000369e27b9) — practical walkthrough
- [Creating Extensions for Compiled Apps](https://madoibito80.github.io/blog/py_winapi_mod/) — Python + Win32 memory reading
- PyMemoryEditor has a built-in tkinter GUI similar to Cheat Engine for memory scanning

**Process**:
1. Run GLM, toggle power off → scan for 0/false
2. Toggle power on → scan for 1/true (filter changed values)
3. Repeat until a single address is isolated
4. Check if address is static or uses pointer chain
5. If pointer chain, need to resolve it at runtime

**Pros vs current approach**:
- Reads the actual internal state — no visual/rendering dependency
- Instant — no screen capture overhead
- Works regardless of window state (minimized, hidden, startup)
- Would completely bypass the startup rendering bug

**Cons**:
- **Address changes every GLM restart** (ASLR) — need pointer chain
- Finding stable pointer chain is labor-intensive reverse engineering
- GLM updates may change memory layout, breaking the chain
- Extremely fragile — the most brittle possible approach
- Requires PROCESS_VM_READ permissions (admin or debug privileges)
- Ethically gray — essentially "hacking" Genelec's software

**Confidence**: LOW — technically feasible but too fragile and maintenance-heavy.

---

## Approach 6: Network/IPC Sniffing

### Technical Feasibility: MEDIUM (with genlc)

**Key discovery**: The [`genlc`](https://github.com/markbergsma/genlc) project has **already reverse-engineered** the Genelec GLM binary protocol!

**What genlc provides**:
- Device discovery, **wakeup & shutdown**, volume setting, mute/unmute, LED control
- Communicates directly with SAM speakers via the GLM adapter (USB-to-serial bridge)
- **Does not require GLM software** for the subset of functionality it implements
- Install: `pip install git+https://github.com/markbergsma/genlc`

**This is potentially game-changing**: If we can use genlc to query the speakers' power state directly (or send wakeup/shutdown commands), we don't need to detect the button state at all.

**GLM's communication**:
- GLM uses a **proprietary binary protocol** over USB (via GLM adapter)
- Genelec has a separate "Smart IP API" for IP-networked speakers, but GLM uses the adapter
- No official REST API or local network API for GLM software itself (confirmed by Genelec community)
- The protocol was reverse-engineered "mostly by sniffing what GLM is sending on the wire"

**JUCE IPC**: JUCE supports named pipes for interprocess communication ([forum thread](https://forum.juce.com/t/interprocess-communication-using-named-pipe/46013)), but there's no evidence GLM exposes any IPC endpoint.

**Pros vs current approach**:
- Could bypass the UI entirely — query speaker state directly
- Not affected by rendering, window state, or display issues
- Well-tested reverse engineering (genlc is a known project)

**Cons**:
- Requires the GLM adapter hardware to be accessible from our process simultaneously
- May conflict with GLM's own communication to the adapter
- genlc may not support all speaker models
- Protocol could change with GLM updates
- Doesn't tell us about GLM's *UI state* — tells us about *speaker state* (which might differ during startup)

**Confidence**: MEDIUM — very promising as a complementary signal, but may conflict with GLM's adapter access.

---

## Approach 7: Windows Message Hooking

### Technical Feasibility: LOW

**The theory**: Hook WM_PAINT or other Windows messages to detect when GLM redraws, and analyze the state from the paint operation.

**Critical problem**: OpenGL apps do NOT use WM_PAINT for rendering. OpenGL renders directly to the window's device context via `wglSwapBuffers`, bypassing the standard Windows paint mechanism. WH_CALLWNDPROCRET only receives messages sent by `SendMessage()`, and WM_PAINT is not sent this way.

**What would be needed**:
- A C/C++ DLL that implements the hook procedure (Python can't be the callback for cross-process hooks)
- SetWindowsHookEx requires the hook to be in a DLL for cross-process monitoring
- Even if we hook messages, we get window messages — not OpenGL render state

**Python integration**: Requires writing a native DLL wrapper that calls Python callbacks. A [Tuts4You forum post](https://forum.tuts4you.com/topic/43561-window-hook-using-python-callback-dll/) describes this pattern.

**Pros vs current approach**:
- Could detect when GLM processes certain messages (focus, resize, etc.)

**Cons**:
- OpenGL doesn't use WM_PAINT — fundamentally wrong approach for render state
- Requires native DLL development
- Complex cross-process hook setup
- Doesn't actually tell us button state

**Confidence**: VERY LOW — wrong tool for the job with OpenGL apps.

---

## Summary & Recommendations

| Approach | Feasibility | Confidence | Effort | Recommendation |
|----------|-------------|------------|--------|----------------|
| 1. UI Automation | Low | Very Low | Low | **Skip** — JUCE doesn't expose child elements |
| 2. Template Matching | High | High | Low | **RECOMMENDED** — best upgrade to current approach |
| 3. GPU Capture API | Medium-High | Medium-High | Medium | **RECOMMENDED** — combine with #2 |
| 4. OpenGL Framebuffer | Low | Low | Very High | **Skip** — absurdly over-engineered |
| 5. Process Memory | Low-Medium | Low | High | **Skip** — too fragile |
| 6. Network/genlc | Medium | Medium | Medium | **INVESTIGATE** — could be complementary |
| 7. Message Hooking | Low | Very Low | High | **Skip** — wrong approach for OpenGL |

### Recommended Strategy

**Phase 1 (Quick Win)**: Replace pixel color classification with OpenCV template matching.
- Keep existing `ImageGrab.grab()` for capture
- Replace `_classify_state()` with `cv2.matchTemplate()` against reference templates
- Add a "startup/loading" template to handle the known startup bug
- Estimated effort: 2-4 hours

**Phase 2 (Reliability Upgrade)**: Replace `ImageGrab` with Windows Graphics Capture API.
- Use `windows-capture` or `DXcam` for faster, GPU-level capture
- Combines well with template matching from Phase 1
- May resolve capture issues during startup/RDP transitions
- Estimated effort: 4-8 hours

**Phase 3 (Investigation)**: Evaluate `genlc` for direct speaker state queries.
- Test if we can query power state without conflicting with GLM
- Could provide a fallback when UI detection returns "unknown"
- Estimated effort: 4-8 hours for evaluation

---

## Sources

### Approach 1 - UI Automation
- [JUCE Windows UI Automation (forum)](https://forum.juce.com/t/windows-ui-automation/57719)
- [JUCE Accessibility on develop](https://forum.juce.com/t/juce-accessibility-on-develop/45142)
- [JUCE Accessibility API Docs](https://docs.juce.com/master/group__juce__gui__basics-accessibility.html)
- [pywinauto accessibility tips](https://github.com/pywinauto/pywinauto/wiki/How-to-enable-accessibility-(tips-and-tricks))

### Approach 2 - Template Matching
- [OpenCV Template Matching Tutorial](https://docs.opencv.org/4.x/de/da9/tutorial_template_matching.html)
- [OpenCV Template Matching - PyImageSearch](https://pyimagesearch.com/2021/03/22/opencv-template-matching-cv2-matchtemplate/)
- [CvMatch - Template matching for any window](https://github.com/Triscuit2311/CvMatch)
- [Game automation with OpenCV](https://www.tautvidas.com/blog/2018/02/automating-basic-tasks-in-games-with-opencv-and-python/)
- [PyAutoGUI + OpenCV integration](https://coderslegacy.com/python/opencv-with-pyautogui-image-recognition/)
- [PyAutoGUI issue #168 - Rewrite with OpenCV](https://github.com/asweigart/pyautogui/issues/168)

### Approach 3 - GPU/DirectX Capture
- [DXGI Desktop Duplication API (Microsoft)](https://learn.microsoft.com/en-us/windows/win32/direct3ddxgi/desktop-dup-api)
- [Windows.Graphics.Capture (Microsoft)](https://learn.microsoft.com/en-us/uwp/api/windows.graphics.capture)
- [windows-capture (PyPI)](https://pypi.org/project/windows-capture/)
- [DXcam (GitHub)](https://github.com/ra1nty/DXcam)
- [BetterCam (GitHub)](https://github.com/RootKit-Org/BetterCam)
- [Fast Window Capture comparison](https://learncodebygaming.com/blog/fast-window-capture)
- [WGC vs DXGI DD (OBS Forum)](https://obsproject.com/forum/threads/windows-graphics-capture-vs-dxgi-desktop-duplication.149320/)

### Approach 4 - OpenGL Framebuffer
- [glReadPixels (Microsoft)](https://learn.microsoft.com/en-us/windows/win32/opengl/glreadpixels)
- [apiparse - DLL hooking based on RenderDoc/apitrace](https://github.com/aschrein/apiparse)
- [How RenderDoc works](https://renderdoc.org/docs/behind_scenes/how_works.html)

### Approach 5 - Process Memory
- [ReadProcessMemory (Microsoft)](https://learn.microsoft.com/en-us/windows/win32/api/memoryapi/nf-memoryapi-readprocessmemory)
- [PyMemoryEditor (PyPI)](https://pypi.org/project/PyMemoryEditor/)
- [Game Hacking with Python + Cheat Engine](https://noob3xploiter.medium.com/game-hacking-with-python-and-cheat-engine-5000369e27b9)
- [Creating Extensions for Compiled Apps](https://madoibito80.github.io/blog/py_winapi_mod/)

### Approach 6 - Network/IPC
- [genlc - Unofficial Genelec SAM Python module](https://github.com/markbergsma/genlc)
- [genlc discussion (ASR Forum)](https://www.audiosciencereview.com/forum/index.php?threads/python-module-to-manage-genelec-sam.25814/)
- [GLM REST API request (Genelec Community)](https://community.genelec.com/forum/-/message_boards/message/1139988)
- [Genelec Smart IP API](https://www.genelec.com/smart-ip-api)
- [JUCE named pipes IPC](https://forum.juce.com/t/interprocess-communication-using-named-pipe/46013)

### Approach 7 - Message Hooking
- [SetWindowsHookEx (Microsoft)](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-setwindowshookexa)
- [SetWindowsHookEx and WM_PAINT (Tek-Tips)](https://www.tek-tips.com/threads/setwindowshookex-and-wm_paint.1080035/)
- [Python + DLL window hooks (Tuts4You)](https://forum.tuts4you.com/topic/43561-window-hook-using-python-callback-dll/)
