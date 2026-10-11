// Browser features the generator expects, for goja (ksef pdf spec §4.2).
// Go defines: __now (render time, ms); __waw(ms) -> [year, month0, day,
// hours, minutes, seconds, ms, weekday, offset minutes] in Europe/Warsaw;
// __wawLocal(y, m0, d, h, mi, s, ms) -> ms; __dtf(ms, withTime, withSeconds);
// __encode(string) -> ArrayBuffer; __decode(bytes, encoding) -> string.
var self = globalThis, window = globalThis;
var navigator = { userAgent: 'gfs' };
var console = (function () {
  function nop() {}
  return { log: nop, info: nop, warn: nop, error: nop, debug: nop, trace: nop };
})();

(function () {
  var D = Date, P = D.prototype;
  var localRe = /^(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d)(?::(\d\d)(?:\.(\d+))?)?$/;
  function parse(s) {
    var m = localRe.exec(String(s).trim());
    if (!m) return D.parse(s);
    var ms = m[7] ? Number((m[7] + '00').slice(0, 3)) : 0;
    return __wawLocal(+m[1], m[2] - 1, +m[3], +m[4], +m[5], m[6] ? +m[6] : 0, ms);
  }
  function W() {
    var a = arguments;
    if (!new.target) return new D(__now).toString();
    if (a.length === 0) return new D(__now);
    if (a.length === 1) {
      if (typeof a[0] === 'string') return new D(parse(a[0]));
      return new D(a[0] instanceof D ? a[0].getTime() : a[0]);
    }
    return new D(__wawLocal(a[0], a[1], a.length > 2 ? a[2] : 1, a[3] || 0, a[4] || 0, a[5] || 0, a[6] || 0));
  }
  W.prototype = P;
  P.constructor = W;
  W.now = function () { return __now; };
  W.parse = parse;
  W.UTC = D.UTC;
  function part(i) {
    return function () {
      var t = this.getTime();
      return t !== t ? NaN : __waw(t)[i];
    };
  }
  P.getFullYear = part(0);
  P.getMonth = part(1);
  P.getDate = part(2);
  P.getHours = part(3);
  P.getMinutes = part(4);
  P.getSeconds = part(5);
  P.getMilliseconds = part(6);
  P.getDay = part(7);
  P.getTimezoneOffset = part(8);
  globalThis.Date = W;
})();

var __timers = [], __timerSeq = 0;
function setTimeout(fn) {
  var id = ++__timerSeq;
  __timers.push({ id: id, fn: fn, args: Array.prototype.slice.call(arguments, 2) });
  return id;
}
function clearTimeout(id) { __timers = __timers.filter(function (t) { return t.id !== id; }); }
var setInterval = setTimeout, clearInterval = clearTimeout;
function setImmediate(fn) { return setTimeout.apply(null, [fn, 0].concat(Array.prototype.slice.call(arguments, 1))); }
var clearImmediate = clearTimeout;
// __runTimers runs every queued timer, including ones queued meanwhile.
function __runTimers() {
  var n = 0;
  while (__timers.length) {
    var t = __timers.shift();
    if (typeof t.fn === 'function') t.fn.apply(null, t.args);
    n++;
  }
  return n;
}

var Intl = {
  DateTimeFormat: function (locale, o) {
    if (!new.target) return new Intl.DateTimeFormat(locale, o);
    this.o = o || {};
  },
  PluralRules: function (locale) {
    if (!new.target) return new Intl.PluralRules(locale);
    this.pl = String(locale).slice(0, 2) === 'pl';
  },
  NumberFormat: function () { throw new Error('Intl.NumberFormat is not available in gfs'); },
  getCanonicalLocales: function (l) { return l === undefined ? [] : [].concat(l); },
};
Intl.DateTimeFormat.prototype.format = function (d) {
  return __dtf(d === undefined ? __now : +d, !!this.o.hour, !!this.o.second);
};
Intl.PluralRules.prototype.select = function (n) {
  if (!this.pl) return n === 1 ? 'one' : 'other';
  if (n === 1) return 'one';
  if (!Number.isInteger(n)) return 'other';
  var m10 = n % 10, m100 = n % 100;
  return m10 >= 2 && m10 <= 4 && !(m100 >= 12 && m100 <= 14) ? 'few' : 'many';
};
Intl.PluralRules.prototype.resolvedOptions = function () {
  return { locale: this.pl ? 'pl' : 'en', pluralCategories: this.pl ? ['one', 'few', 'many', 'other'] : ['one', 'other'] };
};

function TextEncoder() {}
TextEncoder.prototype.encode = function (s) { return new Uint8Array(__encode(s === undefined ? '' : String(s))); };
function TextDecoder(enc) { this.enc = (enc || 'utf-8').toLowerCase(); this.encoding = this.enc; }
TextDecoder.prototype.decode = function (b) { return b === undefined ? '' : __decode(b, this.enc); };

// File holds the XML already decoded by Go: its first bytes read as UTF-8.
function File(text, name) { this._text = text; this.name = name || 'invoice.xml'; }
File.prototype.slice = function () {
  return { arrayBuffer: function () { return Promise.resolve(new Uint8Array([0x3c, 0x3f, 0x78, 0x6d]).buffer); } };
};
function FileReader() {}
FileReader.prototype.readAsText = function (f) {
  var r = this;
  setTimeout(function () { r.result = f._text; r.onload({ target: r }); }, 0);
};
