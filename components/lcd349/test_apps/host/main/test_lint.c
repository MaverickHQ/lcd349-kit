/* SPDX-License-Identifier: Apache-2.0
 * Lint fixtures (task 4.5): for every design rule, a screen that must fire it
 * and a screen that must not, plus the exemption tags.
 */
#include <string.h>

#include "cJSON.h"
#include "emu.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "lcd349.h"
#include "tests.h"
#include "unity.h"

static lv_obj_t *s_scr;

/* A fresh dark screen, loaded and laid out. */
static lv_obj_t *fixture(void)
{
    s_scr = lv_obj_create(NULL);
    lv_obj_set_style_bg_color(s_scr, lv_color_hex(0x0B1622), 0);
    lv_obj_set_style_bg_opa(s_scr, LV_OPA_COVER, 0);
    lv_obj_set_scrollable(s_scr, false);
    lv_screen_load(s_scr);
    return s_scr;
}

/* Lints the screen and counts findings for rule (optionally on objects of class cls). */
static int count(const char *rule, const char *cls)
{
    int errors = 0, warnings = 0;
    lv_obj_update_layout(s_scr);
    cJSON *arr = emu_ui_lint(&errors, &warnings);
    int n = 0;
    const cJSON *f;
    cJSON_ArrayForEach(f, arr)
    {
        const cJSON *r = cJSON_GetObjectItem(f, "rule"), *c = cJSON_GetObjectItem(f, "class");
        if (strcmp(r->valuestring, rule) == 0 && (!cls || strcmp(c->valuestring, cls) == 0)) {
            n++;
        }
    }
    cJSON_Delete(arr);
    return n;
}

static void done(void)
{
    lv_obj_t *old = s_scr;
    lv_screen_load(lv_obj_create(NULL));
    lv_obj_delete(old);
}

static void on_click(lv_event_t *e)
{
    (void)e;
}

static lv_obj_t *button(lv_obj_t *p, int x, int y, int w, int h)
{
    lv_obj_t *b = lv_button_create(p);
    lv_obj_set_pos(b, x, y);
    lv_obj_set_size(b, w, h);
    lv_obj_set_style_bg_color(b, lv_color_hex(0x1565C0), 0);
    return b;
}

static lv_obj_t *text(lv_obj_t *p, const char *t, uint32_t color)
{
    lv_obj_t *l = lv_label_create(p);
    lv_label_set_text(l, t);
    lv_obj_set_style_text_color(l, lv_color_hex(color), 0);
    lv_obj_set_pos(l, 20, 20);
    return l;
}

static void test_lint_touch_target(void)
{
    lcd349_ui_lock();
    lv_obj_t *s = fixture();
    button(s, 20, 20, 40, 40);
    TEST_ASSERT_EQUAL(1, count("touch-target", "button"));
    done();

    s = fixture();
    button(s, 20, 20, 60, 60);
    TEST_ASSERT_EQUAL(0, count("touch-target", NULL));
    done();

    /* A plain container with a click handler counts; one without doesn't,
     * and neither does one whose only handler isn't for input (found in task
     * 8.1: a status bar that freed its state on DELETE failed the rule). */
    s = fixture();
    lv_obj_t *o = lv_obj_create(s);
    lv_obj_set_size(o, 30, 30);
    TEST_ASSERT_EQUAL(0, count("touch-target", NULL));
    lv_obj_add_event_cb(o, on_click, LV_EVENT_DELETE, NULL);
    TEST_ASSERT_EQUAL(0, count("touch-target", NULL));
    lv_obj_add_event_cb(o, on_click, LV_EVENT_CLICKED, NULL);
    TEST_ASSERT_EQUAL(1, count("touch-target", "obj"));
    done();

    /* Exempt: a dense list row, or an explicit ignore tag. */
    s = fixture();
    lv_obj_t *row = button(s, 20, 20, 300, 40);
    lcd349_ui_tag(row, "list-row");
    lv_obj_t *small = button(s, 400, 20, 40, 40);
    lcd349_ui_tag(small, "ignore:touch-target");
    TEST_ASSERT_EQUAL(0, count("touch-target", NULL));
    done();
    lcd349_ui_unlock();
}

static void test_lint_font_min(void)
{
    lcd349_ui_lock();
    lv_obj_t *s = fixture();
    lv_obj_t *l = text(s, "tiny", 0xFFFFFF);
    lv_obj_set_style_text_font(l, &lv_font_montserrat_8, 0);
    TEST_ASSERT_EQUAL(1, count("font-min", "label"));
    done();

    s = fixture();
    text(s, "readable", 0xFFFFFF);
    TEST_ASSERT_EQUAL(0, count("font-min", NULL));
    done();
    lcd349_ui_unlock();
}

static void test_lint_contrast(void)
{
    lcd349_ui_lock();
    lv_obj_t *s = fixture();
    lv_obj_set_style_bg_color(s, lv_color_hex(0x888888), 0);
    text(s, "grey on grey", 0x777777);
    TEST_ASSERT_EQUAL(1, count("contrast", "label"));
    done();

    /* Resolved through the parent: dark text on a light card on a dark screen passes. */
    s = fixture();
    lv_obj_t *card = lv_obj_create(s);
    lv_obj_set_size(card, 300, 100);
    lv_obj_set_style_bg_color(card, lv_color_hex(0xF0F0F0), 0);
    lv_obj_set_style_bg_opa(card, LV_OPA_COVER, 0);
    text(card, "dark on light", 0x101010);
    TEST_ASSERT_EQUAL(0, count("contrast", NULL));
    done();
    lcd349_ui_unlock();
}

static void test_lint_clipped_text(void)
{
    lcd349_ui_lock();
    lv_obj_t *s = fixture();
    lv_obj_t *l = text(s, "this text is far too long for its box", 0xFFFFFF);
    lv_obj_set_size(l, 80, 18); /* wraps to several lines, but only one fits */
    TEST_ASSERT_EQUAL(1, count("clipped-text", "label"));
    done();

    /* Deliberate truncation (dots) and enough room are both fine. */
    s = fixture();
    l = text(s, "this text is far too long for its box", 0xFFFFFF);
    lv_obj_set_size(l, 80, 18);
    lv_label_set_long_mode(l, LV_LABEL_LONG_MODE_DOTS);
    lv_obj_t *l2 = text(s, "fits", 0xFFFFFF);
    lv_obj_set_pos(l2, 20, 80);
    TEST_ASSERT_EQUAL(0, count("clipped-text", NULL));
    done();

    /* A label running out of a small parent is cut off by the parent. */
    s = fixture();
    lv_obj_t *box = lv_obj_create(s);
    lv_obj_set_size(box, 100, 60);
    lv_obj_set_scrollable(box, false);
    lv_obj_set_style_pad_all(box, 0, 0);
    l = text(box, "wide label", 0xFFFFFF);
    lv_obj_set_pos(l, 70, 10);
    TEST_ASSERT_EQUAL(1, count("clipped-text", "label"));
    done();
    lcd349_ui_unlock();
}

static void test_lint_missing_glyph(void)
{
    lcd349_ui_lock();
    lv_obj_t *s = fixture();
    text(s, "ok \xF0\x9F\x98\x80", 0xFFFFFF); /* U+1F600 isn't in Montserrat */
    TEST_ASSERT_EQUAL(1, count("missing-glyph", "label"));
    done();

    s = fixture();
    text(s, "Plain ASCII: 0-9 & symbols " LV_SYMBOL_OK, 0xFFFFFF);
    TEST_ASSERT_EQUAL(0, count("missing-glyph", NULL));
    done();
    lcd349_ui_unlock();
}

static void test_lint_off_screen(void)
{
    lcd349_ui_lock();
    lv_obj_t *s = fixture();
    lv_obj_t *b = button(s, 600, 40, 100, 60); /* runs past x = 639 */
    lv_obj_t *inner = text(b, "child", 0xFFFFFF);
    (void)inner;
    TEST_ASSERT_EQUAL(1, count("off-screen", NULL)); /* the outermost object only */
    done();

    s = fixture();
    button(s, 520, 40, 100, 60);
    TEST_ASSERT_EQUAL(0, count("off-screen", NULL));
    done();

    /* Content of a scrolling list may sit off screen. */
    s = fixture();
    lv_obj_t *list = lv_obj_create(s);
    lv_obj_set_size(list, 300, 172);
    lv_obj_set_scroll_momentum(list, false);
    lv_obj_t *far = button(list, 0, 400, 200, 60);
    (void)far;
    TEST_ASSERT_EQUAL(0, count("off-screen", NULL));
    done();
    lcd349_ui_unlock();
}

static void test_lint_overlap(void)
{
    lcd349_ui_lock();
    lv_obj_t *s = fixture();
    button(s, 100, 50, 60, 60);
    button(s, 140, 50, 60, 60);
    TEST_ASSERT_EQUAL(1, count("overlap", "button"));
    done();

    s = fixture();
    button(s, 100, 50, 60, 60);
    button(s, 170, 50, 60, 60);
    TEST_ASSERT_EQUAL(0, count("overlap", NULL));
    done();

    /* An extended click area counts too. */
    s = fixture();
    lv_obj_t *a = button(s, 100, 50, 60, 60);
    button(s, 170, 50, 60, 60);
    lv_obj_set_ext_click_area(a, 12);
    TEST_ASSERT_EQUAL(1, count("overlap", NULL));
    done();

    /* A dialog's button over a screen button: a clash without a backdrop,
     * none under a modal backdrop, which keeps taps off the screen below. */
    s = fixture();
    button(s, 100, 50, 60, 60);
    lv_obj_t *dialog_btn = button(lv_layer_top(), 120, 60, 60, 60);
    TEST_ASSERT_EQUAL(1, count("overlap", NULL));
    lv_obj_t *backdrop = lv_obj_create(lv_layer_top());
    lv_obj_set_size(backdrop, LCD349_LCD_W, LCD349_LCD_H);
    lv_obj_set_pos(backdrop, 0, 0);
    lv_obj_set_clickable(backdrop, true);
    lv_obj_move_to_index(backdrop, 0);
    TEST_ASSERT_EQUAL(0, count("overlap", NULL));
    lv_obj_delete(backdrop);
    lv_obj_delete(dialog_btn);
    done();
    lcd349_ui_unlock();
}

static void test_lint_no_momentum(void)
{
    lcd349_ui_lock();
    lv_obj_t *s = fixture();
    lv_obj_t *list = lv_obj_create(s);
    lv_obj_set_size(list, 300, 150);
    button(list, 0, 0, 200, 400); /* content overflows: the list really scrolls */
    TEST_ASSERT_EQUAL(1, count("no-momentum", "obj"));
    lv_obj_set_scroll_momentum(list, false);
    TEST_ASSERT_EQUAL(0, count("no-momentum", NULL));
    done();

    /* A container that can't actually scroll is fine with LVGL's default flags. */
    s = fixture();
    lv_obj_t *box = lv_obj_create(s);
    lv_obj_set_size(box, 300, 150);
    TEST_ASSERT_EQUAL(0, count("no-momentum", NULL));
    done();

    /* Nor is a truncated label: its text overflows, but a drag can't reach
     * it, as nothing on it can be pressed (found in task 8.1). */
    s = fixture();
    lv_obj_t *l = text(s, "A long body that wraps over many lines and ends in dots when it runs out of room on the card", 0xFFFFFF);
    lv_obj_set_size(l, 200, 36);
    lv_label_set_long_mode(l, LV_LABEL_LONG_MODE_DOTS);
    TEST_ASSERT_EQUAL(0, count("no-momentum", NULL));
    done();
    lcd349_ui_unlock();
}

static void test_lint_gradient(void)
{
    lcd349_ui_lock();
    lv_obj_t *s = fixture();
    lv_obj_t *o = lv_obj_create(s);
    lv_obj_set_size(o, 200, 100);
    lv_obj_set_style_bg_color(o, lv_color_hex(0xFF0000), 0);
    lv_obj_set_style_bg_grad_color(o, lv_color_hex(0x0000FF), 0);
    lv_obj_set_style_bg_grad_dir(o, LV_GRAD_DIR_VER, 0);
    TEST_ASSERT_EQUAL(1, count("gradient", "obj"));
    done();

    s = fixture();
    o = lv_obj_create(s);
    lv_obj_set_size(o, 200, 100);
    lv_obj_set_style_bg_color(o, lv_color_hex(0xFF0000), 0);
    TEST_ASSERT_EQUAL(0, count("gradient", NULL));
    done();
    lcd349_ui_unlock();
}

static void test_tree_and_find_text(void)
{
    lcd349_ui_lock();
    lv_obj_t *s = fixture();
    lv_obj_t *b = button(s, 100, 50, 120, 60);
    lv_obj_t *l = text(b, "Approve", 0xFFFFFF);
    lv_obj_center(l);
    lcd349_ui_tag(b, "primary");
    lv_obj_update_layout(s);
    int n = 0;
    bool trunc = false;
    cJSON *t = emu_ui_tree(NULL, 32, &n, &trunc);
    TEST_ASSERT_NOT_NULL(t);
    TEST_ASSERT_EQUAL(3, n);
    const cJSON *btn = cJSON_GetArrayItem(cJSON_GetObjectItem(t, "children"), 0);
    TEST_ASSERT_EQUAL_STRING("button", cJSON_GetObjectItem(btn, "class")->valuestring);
    TEST_ASSERT_EQUAL_STRING("screen/0:button", cJSON_GetObjectItem(btn, "path")->valuestring);
    TEST_ASSERT_EQUAL(100, cJSON_GetObjectItem(btn, "x")->valueint);
    TEST_ASSERT_EQUAL_STRING("primary", cJSON_GetArrayItem(cJSON_GetObjectItem(btn, "tags"), 0)->valuestring);
    const cJSON *lab = cJSON_GetArrayItem(cJSON_GetObjectItem(btn, "children"), 0);
    TEST_ASSERT_EQUAL_STRING("Approve", cJSON_GetObjectItem(lab, "text")->valuestring);
    TEST_ASSERT_EQUAL_STRING("#1565C0", cJSON_GetObjectItem(lab, "bg_color")->valuestring);
    cJSON_Delete(t);
    char found[64];
    TEST_ASSERT_TRUE(emu_ui_find_text("Appro", found, sizeof found));
    TEST_ASSERT_EQUAL_STRING("Approve", found);
    TEST_ASSERT_FALSE(emu_ui_find_text("Deny", found, sizeof found));
    lv_obj_set_hidden(b, true);
    TEST_ASSERT_FALSE(emu_ui_find_text("Approve", found, sizeof found)); /* hidden text isn't on screen */
    done();
    lcd349_ui_unlock();
}

/* Paths: an empty one, or only slashes, is the active screen; a segment
 * is digits and an optional :class; anything else finds nothing. */
static void test_tree_paths(void)
{
    lcd349_ui_lock();
    lv_obj_t *s = fixture();
    button(s, 100, 50, 120, 60);
    lv_obj_update_layout(s);
    int n = 0;
    bool trunc = false;
    const char *screen[] = {"", "/", "//", "screen", "screen/"};
    for (size_t i = 0; i < sizeof screen / sizeof screen[0]; i++) {
        n = 0;
        cJSON *t = emu_ui_tree(screen[i], 1, &n, &trunc);
        TEST_ASSERT_NOT_NULL_MESSAGE(t, screen[i]);
        TEST_ASSERT_EQUAL_STRING_MESSAGE("screen", cJSON_GetObjectItem(t, "path")->valuestring, screen[i]);
        cJSON_Delete(t);
    }
    const char *found[] = {"screen/0", "screen/0:button", "/screen/0:button"};
    for (size_t i = 0; i < sizeof found / sizeof found[0]; i++) {
        n = 0;
        cJSON *t = emu_ui_tree(found[i], 1, &n, &trunc);
        TEST_ASSERT_NOT_NULL_MESSAGE(t, found[i]);
        TEST_ASSERT_EQUAL_STRING_MESSAGE("screen/0:button", cJSON_GetObjectItem(t, "path")->valuestring, found[i]);
        cJSON_Delete(t);
    }
    const char *none[] = {"screen/abc", "screen/0x", "screen/-1", "screen/ 0", "screen/:button", "screen/99",
                          "screen/0:label", "nowhere/0", "nowhere"};
    for (size_t i = 0; i < sizeof none / sizeof none[0]; i++) {
        n = 0;
        TEST_ASSERT_NULL_MESSAGE(emu_ui_tree(none[i], 1, &n, &trunc), none[i]);
    }
    done();
    lcd349_ui_unlock();
}

void run_lint_tests(void)
{
    RUN_TEST(test_lint_touch_target);
    RUN_TEST(test_lint_font_min);
    RUN_TEST(test_lint_contrast);
    RUN_TEST(test_lint_clipped_text);
    RUN_TEST(test_lint_missing_glyph);
    RUN_TEST(test_lint_off_screen);
    RUN_TEST(test_lint_overlap);
    RUN_TEST(test_lint_no_momentum);
    RUN_TEST(test_lint_gradient);
    RUN_TEST(test_tree_and_find_text);
    RUN_TEST(test_tree_paths);
}
