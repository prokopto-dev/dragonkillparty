// ios-frame.js — a clean-room iPhone frame for the two phone views in guild-portal.dc.html.
//
// The design tool's original was a third-party JSX starter (unlicensed, see NOTICE); this is our own
// dependency-free replacement exposing the same global the mockup imports:
//
//   <x-import component-from-global-scope="IOSDevice" dark="{{ true }}" hint-size="393px,852px">
//
// It is chrome only. Everything inside — the balance card, the tick strip, the standings rows — is
// authored in the mockup and passed in as children. The frame's job is to make the phone screenshots
// read as phone screenshots: a bezel, a Dynamic Island, a status bar and a home indicator at the
// iPhone 15 Pro logical size of 393 x 852pt.

(() => {
  'use strict';

  const SCREEN_W = 393;
  const SCREEN_H = 852;
  const BEZEL = 11;

  function el(tag, style, text) {
    const node = document.createElement(tag);
    if (style) node.setAttribute('style', style);
    if (text !== undefined) node.textContent = text;
    return node;
  }

  // The status bar is drawn rather than imaged so it needs no assets and inherits the page's font.
  function statusBar(fg) {
    const bar = el(
      'div',
      'position:absolute;top:0;left:0;right:0;height:54px;padding:0 32px;display:flex;' +
        'align-items:center;z-index:2;pointer-events:none;font-size:15px;font-weight:600;' +
        'letter-spacing:-0.01em;color:' + fg + ';font-variant-numeric:tabular-nums',
    );
    bar.appendChild(el('span', 'flex:none;padding-top:6px', '9:41'));
    bar.appendChild(el('span', 'flex:1 0 0'));

    const glyphs = el('span', 'flex:none;display:flex;align-items:center;gap:6px;padding-top:6px');

    // Signal — four ascending bars.
    const signal = el('span', 'display:flex;align-items:flex-end;gap:2px;height:11px');
    [4, 6, 8, 10].forEach((h) => {
      signal.appendChild(
        el('span', 'width:3px;height:' + h + 'px;border-radius:1px;background:' + fg),
      );
    });
    glyphs.appendChild(signal);

    // Wi-Fi — three nested arcs plus a dot, built from bordered quarter-circles.
    const wifi = el('span', 'position:relative;width:16px;height:12px;flex:none');
    [[16, 8, 0], [11, 5.5, 2.5], [6, 3, 5]].forEach(([w, h, left]) => {
      wifi.appendChild(
        el(
          'span',
          'position:absolute;bottom:' + (h > 4 ? 2 : 3) + 'px;left:' + left + 'px;width:' + w + 'px;' +
            'height:' + h + 'px;border:1.6px solid ' + fg + ';border-bottom:none;' +
            'border-radius:' + w + 'px ' + w + 'px 0 0;box-sizing:border-box',
        ),
      );
    });
    glyphs.appendChild(wifi);

    // Battery — shell, nub, fill.
    const battery = el(
      'span',
      'position:relative;width:25px;height:12px;border:1.3px solid ' + fg + ';border-radius:4px;' +
        'opacity:.9;box-sizing:border-box;flex:none',
    );
    battery.appendChild(
      el('span', 'position:absolute;inset:1.6px;width:70%;border-radius:2px;background:' + fg),
    );
    battery.appendChild(
      el(
        'span',
        'position:absolute;right:-3.4px;top:3.6px;width:2px;height:4px;border-radius:0 2px 2px 0;' +
          'background:' + fg + ';opacity:.5',
      ),
    );
    glyphs.appendChild(battery);

    bar.appendChild(glyphs);
    return bar;
  }

  function IOSDevice(props, children) {
    const dark = props.dark !== false;
    const fg = dark ? '#e9e9ed' : '#11121a';
    const screenBg = dark ? '#161826' : '#ffffff';

    const frame = el(
      'div',
      'position:relative;flex:none;width:' + (SCREEN_W + BEZEL * 2) + 'px;' +
        'height:' + (SCREEN_H + BEZEL * 2) + 'px;padding:' + BEZEL + 'px;' +
        'border-radius:66px;background:linear-gradient(160deg,#3a3d4d,#1d1f2b 42%,#33364a);' +
        'box-shadow:0 0 0 1px rgba(0,0,0,.7), 0 24px 60px rgba(0,0,0,.55);box-sizing:content-box',
    );

    const screen = el(
      'div',
      'position:relative;width:' + SCREEN_W + 'px;height:' + SCREEN_H + 'px;overflow:hidden;' +
        'border-radius:55px;background:' + screenBg + ';isolation:isolate',
    );

    // Dynamic Island.
    screen.appendChild(
      el(
        'div',
        'position:absolute;top:11px;left:50%;transform:translateX(-50%);width:124px;height:36px;' +
          'border-radius:20px;background:#000;z-index:3;pointer-events:none',
      ),
    );

    screen.appendChild(statusBar(fg));

    // The authored screen content. Top padding clears the status bar; bottom clears the indicator.
    const content = el(
      'div',
      'position:absolute;inset:0;padding-top:54px;padding-bottom:22px;overflow-y:auto;' +
        'overflow-x:hidden;-webkit-overflow-scrolling:touch',
    );
    if (children) content.appendChild(children);
    screen.appendChild(content);

    // Home indicator.
    screen.appendChild(
      el(
        'div',
        'position:absolute;bottom:8px;left:50%;transform:translateX(-50%);width:139px;height:5px;' +
          'border-radius:3px;background:' + fg + ';opacity:.55;z-index:3;pointer-events:none',
      ),
    );

    frame.appendChild(screen);
    return frame;
  }

  window.IOSDevice = IOSDevice;
})();
