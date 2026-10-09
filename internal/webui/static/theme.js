'use strict';

// The palette is applied here, in a script the page loads before it draws
// anything, rather than in app.js, which is deferred: from there the panel
// would paint its default colours first and swap them a moment later. The
// same reason the page starts hidden - see body.booting in app.css.
(function () {
  try {
    const saved = localStorage.getItem('cm-theme');
    if (saved) document.documentElement.dataset.theme = saved;
  } catch (e) {
    // A private window has no storage. The default palette stands.
  }
})();
