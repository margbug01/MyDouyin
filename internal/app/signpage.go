package app

// signPage is a lightweight same-origin HTML host loaded at douyinHome for
// Douyin write requests and pageGet reads. It loads only Douyin's official
// security scripts (secsdk, webmssdk, sdk-glue/bdms, ucSecureConsumerSDK for
// bd-ticket-guard, and DTraitSDK for x-tt-session-dtrait) without the 25 MB
// React and xgplayer application bundle. If the ticket-guard signer does not
// reach "ready", web.page falls back to loading the full douyinHome page.
const signPage = `<!DOCTYPE html><html><head><meta charset="utf-8">
<script>window.__mdSignPage = true;</script>
<script src="https://lf-security.bytegoofy.com/obj/security-secsdk/runtime_bundler_34.js" project-id="34" crossorigin="anonymous"></script>
<script src="https://lf-c-flwb.bytetos.com/obj/rc-client-security/c-webmssdk/1.0.0.20/webmssdk.es5.js" crossorigin="anonymous"></script>
<script src="https://lf-c-flwb.bytetos.com/obj/rc-client-security/web/glue/1.0.0.64-fix.01/sdk-glue.js" data-sdk-glue-default="load" crossorigin="anonymous"></script>
<script>
(function() {
  if (!window._SdkGlueInit) return;
  let ddrt = 8.5, ic = 8.5;
  if (navigator && navigator.hardwareConcurrency <= 4 && navigator.deviceMemory <= 2) {
    ddrt = 10;
    ic = 10;
  }
  window._SdkGlueInit({
    self: { aid: 6383, pageId: 6241 },
    bdms: {
      aid: 6383,
      pageId: 6241,
      paths: ["^/webcast/", "^/aweme/v1/", "^/aweme/v2/", "/douplus/", "^/api/ad/v1/inspire", "/v1/message/send", "^/live/", "^/captcha/", "^/ecom/", "^/luna/pc"],
      boe: false,
      ddrt: ddrt,
      ic: ic
    },
    verifyCenter: {
      interceptPathList: {
        include: ["^/webcast/", "^/aweme/v1/", "^/aweme/v2/", "/douplus/", "/v1/message/send", "^/live/", "^/captcha/", "^/ecom/", "^/api/ad/v1/inspire"],
        exclude: ["^/aweme/v1/play/", "douyinvod\\.com"]
      },
      commonOptions: { aid: 6383, pageId: 6241 },
      nocaptchaOptions: { enabled: true },
      captchaOptions: { ele: "captcha_container", showMode: "mask" }
    }
  }, {
    verifyCenter: { srcList: ["https://lf-rc1.yhgfb-cn-static.com/obj/rc-verifycenter/sec_sdk_build/4.0.28/captcha/index.js", "https://lf-rc2.yhgfb-cn-static.com/obj/rc-verifycenter/sec_sdk_build/4.0.28/captcha/index.js"] },
    bdms: { srcList: ["https://p-pc-weboff.byteimg.com/tos-cn-i-9r5gewecjs/bdms_1.0.1.19_fix.js", "https://lf-c-flwb.bytetos.com/obj/rc-client-security/web/stable/1.0.1.19-fix.01/bdms.js", "https://lf-headquarters-speed.yhgfb-cn-static.com/obj/rc-client-security/web/stable/1.0.1.19-fix.01/bdms.js"] }
  });
})();
</script>
<script src="https://p-pc-weboff.byteimg.com/tos-cn-i-9r5gewecjs/bdms_1.0.1.19_fix.js" no-entry="true"></script>
<script src="https://lf-douyin-pc-web.douyinstatic.com/obj/passport-fe/ucenter_fe/byted/secure-consumer-sdk/douyin/1.0.0.13/bootstrap.umd.production.js" crossorigin="anonymous"></script>
<script src="https://lf-douyin-pc-web.douyinstatic.com/obj/passport-fe/ucenter_fe/@byted/uc-secure-dtrait-core/1.0.0.16/dist/index.umd.production.js" crossorigin="anonymous"></script>
<script>
(function() {
  const b2b64 = (u) => { let s = ""; for (let i = 0; i < u.length; i++) s += String.fromCharCode(u[i]); return btoa(s); };
  const b642b = (s) => { const t = atob(s), u = new Uint8Array(t.length); for (let i = 0; i < t.length; i++) u[i] = t.charCodeAt(i); return u; };
  const concat = (arr) => { const out = new Uint8Array(arr.reduce((a, b) => a + b.byteLength, 0)); let o = 0; for (const a of arr) { out.set(new Uint8Array(a), o); o += a.byteLength; } return out; };
  const u2hex = (u) => Array.from(u, b => b.toString(16).padStart(2, "0")).join("");
  const hex2u = (s) => new Uint8Array(s.match(/.{2}/g).map(x => parseInt(x, 16)));
  const rand = (u) => crypto.getRandomValues(u);
  const parseQ = (s) => { const r = {}; if (!s) return r; (s[0] === "?" ? s.slice(1) : s).split("&").forEach(p => { const [k, v = ""] = p.split("="); r[k] = decodeURIComponent(v); }); return r; };

  window.DTraitUcCryptoJSUtil = {
    parseQuery: parseQ,
    getQuery: (u) => !u ? parseQ(location.search.slice(1)) : (u.split("?").length < 2 ? {} : parseQ(u.split("?").pop().split("#")[0])),
    isSupportSystemCrypto: () => !!(window.crypto && window.crypto.subtle),
    bufferConcat: concat,
    buff2base64: b2b64,
    base642buff: b642b,
    uint8ArrayToHex: u2hex,
    getRandomValues: rand,
    hexToUint8Array: hex2u,
    unit8ArrayToString: (u) => { let s = ""; for (const b of u) s += String.fromCharCode(b); return s; }
  };

  window.DTraitUcAesEncrypt = {
    supportSystemCrypto: true,
    getAesKey: () => rand(new Uint8Array(16)),
    ensureInit: () => Promise.resolve(true),
    encryptData: async (hexKey, plain) => {
      const iv = rand(new Uint8Array(16));
      const key = await crypto.subtle.importKey("raw", hex2u(hexKey), { name: "AES-CBC" }, false, ["encrypt"]);
      const ct = new Uint8Array(await crypto.subtle.encrypt({ name: "AES-CBC", iv }, key, new TextEncoder().encode(plain)));
      return { cipherText: b2b64(concat([iv, ct])), encryptedData: b2b64(ct), iv: b2b64(iv) };
    }
  };

  function readDerLen(b, o) {
    let l = b[o++];
    if (l < 0x80) return [l, o];
    const n = l & 0x7f;
    l = 0;
    for (let i = 0; i < n; i++) l = (l << 8) | b[o++];
    return [l, o];
  }

  function parseRsaPub(pem) {
    const b = b642b(pem.replace(/-----[^-]+-----/g, "").replace(/\s+/g, ""));
    let o = 0;
    const readInt = () => {
      if (b[o++] !== 0x02) throw new Error("asn1 int");
      const [l, next] = readDerLen(b, o);
      let s = next, e = next + l;
      while (s < e && b[s] === 0) s++;
      let v = 0n;
      for (let i = s; i < e; i++) v = (v << 8n) | BigInt(b[i]);
      o = e;
      return [v, e - s];
    };
    if (b[o++] !== 0x30) throw new Error("asn1 seq");
    o = readDerLen(b, o)[1];
    if (b[o] === 0x30) {
      o++;
      const [al, an] = readDerLen(b, o);
      o = an + al;
      if (b[o++] !== 0x03) throw new Error("asn1 bitstr");
      o = readDerLen(b, o)[1] + 1;
      if (b[o++] !== 0x30) throw new Error("asn1 inner seq");
      o = readDerLen(b, o)[1];
    }
    const [n, k] = readInt();
    const [e] = readInt();
    return { n, e, k };
  }

  function modPow(base, exp, mod) {
    let r = 1n;
    base %= mod;
    while (exp > 0n) {
      if (exp & 1n) r = (r * base) % mod;
      exp >>= 1n;
      base = (base * base) % mod;
    }
    return r;
  }

  window.DTraitUcRsaEncrypt = {
    ensureInit: () => Promise.resolve(true),
    encryptData: async (pem, plain) => {
      const { n, e, k } = parseRsaPub(pem);
      const msg = new TextEncoder().encode(plain);
      if (msg.length > k - 11) return "";
      const em = new Uint8Array(k);
      em[0] = 0x00;
      em[1] = 0x02;
      const psLen = k - msg.length - 3;
      for (let i = 0; i < psLen; i++) {
        let r = 0;
        while (r === 0) {
          const t = new Uint8Array(1);
          crypto.getRandomValues(t);
          r = t[0];
        }
        em[2 + i] = r;
      }
      em[2 + psLen] = 0x00;
      em.set(msg, 3 + psLen);
      let m = 0n;
      for (let i = 0; i < k; i++) m = (m << 8n) | BigInt(em[i]);
      let c = modPow(m, e, n);
      const out = new Uint8Array(k);
      for (let i = k - 1; i >= 0; i--) {
        out[i] = Number(c & 0xffn);
        c >>= 8n;
      }
      return b2b64(out);
    }
  };

  if (window.ucSecureConsumerSDK && window.ucSecureConsumerSDK.start) {
    window.ucSecureConsumerSDK.start({
      aid: 6383,
      scene: "web_protect",
      consumerPathList: [
        "/2/user/info/",
        "/aweme/v1/report/submit",
        "/webcast/user/report/commit",
        "/v1/message/send",
        "/aweme/v1/web/commit/item/digg",
        "/aweme/v1/web/cancel/item/digg/multi",
        "/aweme/v1/web/commit/follow/user",
        "/aweme/v1/web/comment/publish",
        "/aweme/v1/web/commit/user",
        "/aweme/v1/web/collects/maintain",
        "/aweme/v1/web/collects/video/move",
        "/aweme/v1/web/aweme/collect",
        "/aweme/v1/web/music/collect",
        "/aweme/v1/web/mix/collect",
        "/aweme/v1/web/series/collect",
        "/aweme/v1/web/comment/list/reply",
        "/aweme/v1/web/comment/list",
        "/aweme/v2/web/comment/list/reply",
        "/webcast/room/chat",
        "/aweme/v1/web/danmaku/post_v2",
        "/webcast/user/relation/update",
        "/aweme/v1/web/comment/digg",
        "/aweme/v1/web/aweme/favorite",
        "/webcast/gift/send",
        "/aweme/v1/web/danmaku/digg"
      ],
      timeoutMs: 5000,
      enableEcdh: true,
      enableTrustedTimestamp: true
    });
  }

  let dtraitParams = {
    urlVersion: "1.0.0.16",
    centralRsaPub: "LS0tLS1CRUdJTiBSU0EgUFVCTElDIEtFWS0tLS0tCk1JSUJDZ0tDQVFFQTQrZHZ2WTd1TStvcGMrbkxHL0R1bVNlRm83YVZjSW0xTE8rbVVJcldwclJ6UDBhMUdwRVEKNHF0TzlNUmYvbHdFSXgzOCs0Qlo0WE9HemV2VnR1VXZmSU9VRTdBVHRRVzdGS0pmNVBuU0xDSTYvazB2bDFGQwpMVVNWbUVQNnFQSnJJalo0elhvcWkzeXVOWisxb2RiUkEvL0dIZ2NnU3l5eWFMcXp3amtwV0dYb3VNWW12WXNTCnBway9mdjJFV0FCc3RQTnhXYTRFT0JDYWRUVVBrWE5RNzZOQkVQOXh6ZkpTMjB3aUR2MW9TL3ZLdnJTVXBXY0oKbmF6a2tCdnFRYmJBcVZiUUZURi9EUGlrcHB1NlpUNmxHSVh2SktDcmVlRmlIQTJxSzZ0UzE4U1dWSFc5QVJ6MQorcGpCMWVxSUlZdG9oV3BUMkI0ME9DNE84dFZlQkFuYmlRSURBUUFCCi0tLS0tRU5EIFJTQSBQVUJMSUMgS0VZLS0tLS0=",
    centralVersion: "d0",
    edgeRsaPub: "LS0tLS1CRUdJTiBSU0EgUFVCTElDIEtFWS0tLS0tCk1JSUJDZ0tDQVFFQXlFQkQ0MXQzcWpqL1NOaU5rT3BBbnNGdGZKZ0F5MGF5VTZCbEJ3RS9EZVZjNkdWV0xWUk4KWjdiMWRuRHVmQk5iUm1XQjlZeWVyYm1FOFFDM2lPOXp1NVFWd2x4SGV2ZEN0ZFFyeDZpQzF3QVRoaHFjdTNIYgprZ1dsazZ1Ylk5MXRvRFhNd0k2WGdmRUoyVEJsdHVSbklXRjR5RDVEaEc2c3lSSVNmNTRMWGY0WjgzbzlGcXNvCmlsNkV3cVZCbEU3dXlIY3dJOTA5WDg4Rlc3MXFLdmJMU040OGJlQ0EwbzFmZitqbmhRakNBTDZqbUR2dUhJeWEKUk1vYm1wRFVOLzQ3L3NHbDNzNDlFOEZFSEFXUmk5d1cyc2NZUDBJTkJXUlR5RlRHcG9GUGlqekJFUndnYzdrWQozVno3ZytSMXd2RkxUSEVITEtYUWFwTHpEMWR5Uk81YUt3SURBUUFCCi0tLS0tRU5EIFJTQSBQVUJMSUMgS0VZLS0tLS0K",
    edgeVersion: "d0",
    dTraitVersion: "0"
  };
  try {
    const cached = localStorage.getItem("dtrait-sdk/s_sdk_server_cert_key");
    if (cached) {
      const parsed = JSON.parse(atob(cached));
      if (parsed && parsed.centralRsaPub && parsed.edgeRsaPub) {
        dtraitParams = Object.assign({}, dtraitParams, parsed);
      }
    }
  } catch {}

  if (window.DTraitSDK) {
    (window.DTraitSDK.default || window.DTraitSDK).getInstance(dtraitParams, {
      dTraitPath: [
        "/aweme/v1/web/commit/item/digg",
        "/aweme/v1/web/commit/follow/user",
        "/aweme/v1/web/comment/publish",
        "/aweme/v1/web/commit/user",
        "/aweme/v1/web/danmaku/post_v2",
        "/v1/message/send"
      ],
      dTraitHost: [],
      urlRewriteRules: [],
      containerSdkVersion: "1.1.19",
      libraGroup: "",
      delayCollect: 0,
      monitor: { sendSlardarEvent: () => {}, sendSlardarLog: () => {}, sendTeaLog: () => {} }
    });
  }
  window.__mdSignReady = true;
})();
</script>
</head><body><div id="captcha_container"></div></body></html>`
