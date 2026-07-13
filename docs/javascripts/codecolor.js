// Two cosmetic passes that paint with the four garden accents, both keyed off
// a data attribute extra.css styles. A single MutationObserver re-runs them
// after zensical's instant navigation swaps in new page content.
//
//  1. paintSh — tint shell (`sh`) code fences, cycling per block so consecutive
//     blocks differ. The pygments shell lexer leaves commands as bare text, so
//     the blocks would otherwise render in one drab grey.
//  2. paintTableCode — stripe the inline-code chips in reference tables by row,
//     so the long label/flag tables read as alternating bands instead of a
//     wall of identical grey chips.
(function () {
    var COLORS = ["green", "seedling", "amber", "soil"];
    var nextSh = 0;

    function paintSh() {
        document.querySelectorAll(".language-sh.highlight:not([data-pt-accent])").forEach(function (block) {
            block.setAttribute("data-pt-accent", COLORS[nextSh % COLORS.length]);
            nextSh++;
        });
    }

    // Every <code> in a tbody row shares one hue; rows cycle through the palette.
    // The cycle resets per table (data-pt-ic-done marks a table painted) so the
    // first row of every table starts on the same colour.
    function paintTableCode() {
        document.querySelectorAll(".md-typeset table:not([data-pt-ic-done])").forEach(function (table) {
            table.querySelectorAll("tbody tr").forEach(function (row, i) {
                var hue = COLORS[i % COLORS.length];
                row.querySelectorAll("code").forEach(function (code) {
                    code.setAttribute("data-pt-ic", hue);
                });
            });
            table.setAttribute("data-pt-ic-done", "");
        });
    }

    function paint() {
        paintSh();
        paintTableCode();
    }

    paint();
    // childList/subtree only — the setAttribute calls above mutate attributes,
    // which this observer ignores, so painting never re-triggers itself.
    new MutationObserver(paint).observe(document.body, {
        childList: true,
        subtree: true,
    });
})();
