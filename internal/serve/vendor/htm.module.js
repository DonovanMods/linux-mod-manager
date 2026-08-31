// VENDORED THIRD-PARTY CODE - DO NOT EDIT.
//
// htm - JSX-like tagged-template syntax with no build step, which is what
// lets this SPA be plain ES modules a browser loads directly. It parses
// template literals at runtime; it uses no eval and no Function
// constructor, so the page needs no unsafe-eval in its CSP.
//
// Package:  htm
// Version:  3.1.1 (pinned)
// Source:   https://unpkg.com/htm@3.1.1/dist/htm.module.js
// SHA-256:  ab33dd3f38059b9be4d5f5350128eefb2356639c4e0bbe9d9e8b3ba75847e9e4
//           (of the UPSTREAM artifact as fetched, byte for byte)
//
// This file is that artifact verbatim, with 1 local edit:
//  1. a header comment (these lines)
//
// To re-verify: strip the header block above (and undo the edit) and
// compare sha256 against the value recorded here, or re-fetch the
// Source URL. Nothing in this repo fetches it: `go build` is the whole
// build (docs/plans/2026-08-31-webui-impl.md §Global Constraints - no
// Node, no npm, no bundler, ever), and the file is served from the
// binary by go:embed.
var n=function(t,s,r,e){var u;s[0]=0;for(var h=1;h<s.length;h++){var p=s[h++],a=s[h]?(s[0]|=p?1:2,r[s[h++]]):s[++h];3===p?e[0]=a:4===p?e[1]=Object.assign(e[1]||{},a):5===p?(e[1]=e[1]||{})[s[++h]]=a:6===p?e[1][s[++h]]+=a+"":p?(u=t.apply(a,n(t,a,r,["",null])),e.push(u),a[0]?s[0]|=2:(s[h-2]=0,s[h]=u)):e.push(a)}return e},t=new Map;export default function(s){var r=t.get(this);return r||(r=new Map,t.set(this,r)),(r=n(this,r.get(s)||(r.set(s,r=function(n){for(var t,s,r=1,e="",u="",h=[0],p=function(n){1===r&&(n||(e=e.replace(/^\s*\n\s*|\s*\n\s*$/g,"")))?h.push(0,n,e):3===r&&(n||e)?(h.push(3,n,e),r=2):2===r&&"..."===e&&n?h.push(4,n,0):2===r&&e&&!n?h.push(5,0,!0,e):r>=5&&((e||!n&&5===r)&&(h.push(r,0,e,s),r=6),n&&(h.push(r,n,0,s),r=6)),e=""},a=0;a<n.length;a++){a&&(1===r&&p(),p(a));for(var l=0;l<n[a].length;l++)t=n[a][l],1===r?"<"===t?(p(),h=[h],r=3):e+=t:4===r?"--"===e&&">"===t?(r=1,e=""):e=t+e[0]:u?t===u?u="":e+=t:'"'===t||"'"===t?u=t:">"===t?(p(),r=1):r&&("="===t?(r=5,s=e,e=""):"/"===t&&(r<5||">"===n[a][l+1])?(p(),3===r&&(h=h[0]),r=h,(h=h[0]).push(2,0,r),r=0):" "===t||"\t"===t||"\n"===t||"\r"===t?(p(),r=2):e+=t),3===r&&"!--"===e&&(r=4,h=h[0])}return p(),h}(s)),r),arguments,[])).length>1?r:r[0]}
