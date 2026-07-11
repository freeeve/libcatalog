// LOC MARC 21 field documentation links: each known tag maps to
// its loc.gov page (bd245.html, adleader.html, ...). Only documented tags
// link -- local/undefined tags render as plain text rather than a 404 --
// and the grouped local ranges use their shared pages (09X, 59X).

/** Which MARC 21 format a record follows, for documentation routing. */
export type MarcDocKind = "bibliographic" | "authority";

/** Tags with their own page under loc.gov/marc/bibliographic/. */
const BIB_TAGS = new Set(
  (
    "001 003 005 006 007 008 " +
    "010 013 015 016 017 018 020 022 024 025 026 027 028 030 031 032 033 034 " +
    "035 036 037 038 040 041 042 043 044 045 046 047 048 050 051 052 055 060 " +
    "061 066 070 071 072 074 080 082 083 084 085 086 088 " +
    "100 110 111 130 " +
    "210 222 240 242 243 245 246 247 250 251 254 255 256 257 258 260 261 262 " +
    "263 264 265 270 " +
    "300 306 307 310 321 334 335 336 337 338 340 341 342 343 344 345 346 347 " +
    "348 351 352 355 357 361 362 363 365 366 370 377 380 381 382 383 384 385 " +
    "386 388 490 " +
    "500 501 502 504 505 506 507 508 510 511 513 514 515 516 518 520 521 522 " +
    "524 525 526 530 532 533 534 535 536 538 540 541 542 544 545 546 547 550 " +
    "552 555 556 561 562 563 565 567 580 581 583 584 585 586 588 " +
    "600 610 611 630 647 648 650 651 653 654 655 656 657 658 662 688 " +
    "700 710 711 720 730 740 751 752 753 754 758 760 762 765 767 770 772 773 " +
    "774 775 776 777 780 785 786 787 788 " +
    "800 810 811 830 841 842 843 844 845 850 852 853 854 855 856 857 863 864 " +
    "865 866 867 868 876 877 878 880 882 883 884 885 886 887"
  ).split(" "),
);

/** Tags with their own page under loc.gov/marc/authority/. */
const AUTH_TAGS = new Set(
  (
    "001 003 005 008 " +
    "010 014 016 020 022 023 024 031 034 035 040 042 043 045 046 050 052 053 " +
    "055 060 065 066 070 072 073 075 080 082 083 086 087 088 " +
    "100 110 111 130 147 148 150 151 155 162 180 181 182 185 " +
    "260 263 336 348 360 368 370 371 372 373 374 375 376 377 378 380 381 382 " +
    "383 384 385 386 388 " +
    "400 410 411 430 447 448 450 451 455 462 480 481 482 485 " +
    "500 510 511 530 547 548 550 551 555 562 580 581 582 585 " +
    "640 641 642 643 644 645 646 663 664 665 666 667 670 672 675 677 678 680 " +
    "681 682 688 " +
    "700 710 711 730 747 748 750 751 755 762 780 781 782 785 788 " +
    "852 856 880 883 884 885"
  ).split(" "),
);

/** The loc.gov MARC 21 documentation URL for a tag ("LDR" for the leader),
 *  or undefined when no page exists (local and undefined tags). */
export function locFieldHelpUrl(tag: string, kind: MarcDocKind = "bibliographic"): string | undefined {
  const base = kind === "authority" ? "https://www.loc.gov/marc/authority/ad" : "https://www.loc.gov/marc/bibliographic/bd";
  if (tag.toUpperCase() === "LDR") return base + "leader.html";
  if (!/^\d{3}$/.test(tag)) return undefined;
  if (kind === "bibliographic") {
    if (tag >= "090" && tag <= "099") return base + "09x.html";
    if (tag >= "590" && tag <= "599") return base + "59x.html";
    return BIB_TAGS.has(tag) ? base + tag + ".html" : undefined;
  }
  return AUTH_TAGS.has(tag) ? base + tag + ".html" : undefined;
}
