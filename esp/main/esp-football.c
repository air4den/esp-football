#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/event_groups.h"
#include "driver/gpio.h"
#include "esp_lcd_panel_io.h"
#include "esp_lcd_panel_vendor.h"
#include "esp_lcd_panel_ops.h"
#include "esp_lvgl_port.h"
#include "esp_wifi.h"
#include "esp_event.h"
#include "esp_log.h"
#include "esp_netif.h"
#include "esp_http_client.h"
#include "nvs_flash.h"
#include "cJSON.h"
#include "lvgl.h"

// ── Pin Definitions ───────────────────────────────────────────────────────────
#define LCD_HOST       SPI2_HOST
#define PIN_NUM_PWR    7
#define PIN_NUM_SCK    36
#define PIN_NUM_MOSI   35
#define PIN_NUM_CS     42
#define PIN_NUM_DC     40
#define PIN_NUM_RST    41
#define PIN_NUM_BKLT   45

// ── WiFi config (set in sdkconfig.defaults.local) ─────────────────────────────
#define WIFI_SSID      CONFIG_FOOTBALL_WIFI_SSID
#define WIFI_PASS      CONFIG_FOOTBALL_WIFI_PASSWORD
#define LAMBDA_URL     CONFIG_FOOTBALL_LAMBDA_URL
#define WIFI_MAX_RETRY 10

// ── WiFi event group ──────────────────────────────────────────────────────────
#define WIFI_CONNECTED_BIT BIT0
#define WIFI_FAIL_BIT      BIT1

static EventGroupHandle_t s_wifi_event_group;
static int s_retry_num = 0;
static const char *TAG = "esp-football";

// ── WiFi event handler ────────────────────────────────────────────────────────
static void wifi_event_handler(void *arg, esp_event_base_t event_base,
                                int32_t event_id, void *event_data)
{
    if (event_base == WIFI_EVENT && event_id == WIFI_EVENT_STA_START) {
        esp_wifi_connect();
    } else if (event_base == WIFI_EVENT && event_id == WIFI_EVENT_STA_DISCONNECTED) {
        if (s_retry_num < WIFI_MAX_RETRY) {
            esp_wifi_connect();
            s_retry_num++;
            ESP_LOGW(TAG, "WiFi disconnected, retrying (%d/%d)...", s_retry_num, WIFI_MAX_RETRY);
        } else {
            xEventGroupSetBits(s_wifi_event_group, WIFI_FAIL_BIT);
            ESP_LOGE(TAG, "WiFi connection failed after %d retries", WIFI_MAX_RETRY);
        }
    } else if (event_base == IP_EVENT && event_id == IP_EVENT_STA_GOT_IP) {
        ip_event_got_ip_t *event = (ip_event_got_ip_t *)event_data;
        ESP_LOGI(TAG, "WiFi connected, IP: " IPSTR, IP2STR(&event->ip_info.ip));
        s_retry_num = 0;
        xEventGroupSetBits(s_wifi_event_group, WIFI_CONNECTED_BIT);
    }
}

// ── WiFi init ─────────────────────────────────────────────────────────────────
// Returns true if connected, false on failure.
static bool wifi_init_sta(void)
{
    s_wifi_event_group = xEventGroupCreate();

    ESP_ERROR_CHECK(esp_netif_init());
    ESP_ERROR_CHECK(esp_event_loop_create_default());
    esp_netif_create_default_wifi_sta();

    wifi_init_config_t cfg = WIFI_INIT_CONFIG_DEFAULT();
    ESP_ERROR_CHECK(esp_wifi_init(&cfg));

    esp_event_handler_instance_t instance_any_id;
    esp_event_handler_instance_t instance_got_ip;
    ESP_ERROR_CHECK(esp_event_handler_instance_register(
        WIFI_EVENT, ESP_EVENT_ANY_ID, &wifi_event_handler, NULL, &instance_any_id));
    ESP_ERROR_CHECK(esp_event_handler_instance_register(
        IP_EVENT, IP_EVENT_STA_GOT_IP, &wifi_event_handler, NULL, &instance_got_ip));

    wifi_config_t wifi_config = {
        .sta = {
            .ssid     = WIFI_SSID,
            .password = WIFI_PASS,
            .threshold.authmode = WIFI_AUTH_WPA2_PSK,
        },
    };
    ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_STA));
    ESP_ERROR_CHECK(esp_wifi_set_config(WIFI_IF_STA, &wifi_config));
    ESP_ERROR_CHECK(esp_wifi_start());

    ESP_LOGI(TAG, "Connecting to SSID: %s", WIFI_SSID);

    EventBits_t bits = xEventGroupWaitBits(s_wifi_event_group,
        WIFI_CONNECTED_BIT | WIFI_FAIL_BIT,
        pdFALSE, pdFALSE, portMAX_DELAY);

    if (bits & WIFI_CONNECTED_BIT) {
        return true;
    }
    return false;
}

// ── HTTP response buffer ──────────────────────────────────────────────────────
#define HTTP_BUF_SIZE 2048

typedef struct {
    char  buf[HTTP_BUF_SIZE];
    int   len;
} http_buf_t;

static esp_err_t http_event_handler(esp_http_client_event_t *evt)
{
    http_buf_t *resp = (http_buf_t *)evt->user_data;
    if (evt->event_id == HTTP_EVENT_ON_DATA) {
        int copy_len = evt->data_len;
        if (resp->len + copy_len >= HTTP_BUF_SIZE - 1) {
            copy_len = HTTP_BUF_SIZE - 1 - resp->len;
        }
        memcpy(resp->buf + resp->len, evt->data, copy_len);
        resp->len += copy_len;
        resp->buf[resp->len] = '\0';
    }
    return ESP_OK;
}

// ── Poll task ─────────────────────────────────────────────────────────────────
// Queries the Lambda, parses the JSON, logs the result.
// In future phases this will update LVGL widgets.
static void poll_task(void *pvParameters)
{
    static http_buf_t resp;

    while (1) {
        resp.len = 0;
        memset(resp.buf, 0, sizeof(resp.buf));

        esp_http_client_config_t config = {
            .url            = LAMBDA_URL,
            .event_handler  = http_event_handler,
            .user_data      = &resp,
            .timeout_ms     = 10000,
        };

        esp_http_client_handle_t client = esp_http_client_init(&config);
        esp_err_t err = esp_http_client_perform(client);

        int sleep_seconds = 60; // fallback

        if (err == ESP_OK) {
            int status = esp_http_client_get_status_code(client);
            ESP_LOGI(TAG, "HTTP %d, %d bytes", status, resp.len);

            cJSON *root = cJSON_Parse(resp.buf);
            if (root) {
                cJSON *gs = cJSON_GetObjectItem(root, "game_state");
                cJSON *ss = cJSON_GetObjectItem(root, "sleep_seconds");

                if (cJSON_IsString(gs)) {
                    ESP_LOGI(TAG, "game_state: %s", gs->valuestring);
                }
                if (cJSON_IsNumber(ss)) {
                    sleep_seconds = ss->valueint;
                    ESP_LOGI(TAG, "next poll in %d seconds", sleep_seconds);
                }

                // Log current match if present
                cJSON *cm = cJSON_GetObjectItem(root, "current_match");
                if (cJSON_IsObject(cm)) {
                    cJSON *home  = cJSON_GetObjectItem(cm, "home_team");
                    cJSON *away  = cJSON_GetObjectItem(cm, "away_team");
                    cJSON *hs    = cJSON_GetObjectItem(cm, "home_score");
                    cJSON *as_   = cJSON_GetObjectItem(cm, "away_score");
                    cJSON *clock = cJSON_GetObjectItem(cm, "match_clock");
                    ESP_LOGI(TAG, "Match: %s %s - %s %s  [%s]",
                        cJSON_IsString(home)  ? home->valuestring  : "?",
                        cJSON_IsString(hs)    ? hs->valuestring    : "?",
                        cJSON_IsString(as_)   ? as_->valuestring   : "?",
                        cJSON_IsString(away)  ? away->valuestring  : "?",
                        cJSON_IsString(clock) ? clock->valuestring : "?");
                }

                // Log next match if present
                cJSON *nm = cJSON_GetObjectItem(root, "next_match");
                if (cJSON_IsObject(nm)) {
                    cJSON *opp  = cJSON_GetObjectItem(nm, "opponent");
                    cJSON *date = cJSON_GetObjectItem(nm, "date");
                    cJSON *time = cJSON_GetObjectItem(nm, "time");
                    ESP_LOGI(TAG, "Next match vs %s on %s at %s",
                        cJSON_IsString(opp)  ? opp->valuestring  : "?",
                        cJSON_IsString(date) ? date->valuestring : "?",
                        cJSON_IsString(time) ? time->valuestring : "?");
                }

                cJSON_Delete(root);
            } else {
                ESP_LOGE(TAG, "JSON parse failed");
            }
        } else {
            ESP_LOGE(TAG, "HTTP request failed: %s", esp_err_to_name(err));
        }

        esp_http_client_cleanup(client);

        ESP_LOGI(TAG, "Sleeping %d seconds until next poll", sleep_seconds);
        vTaskDelay(pdMS_TO_TICKS((uint32_t)sleep_seconds * 1000));
    }
}

// ── app_main ──────────────────────────────────────────────────────────────────
void app_main(void)
{
    // 1. NVS (required by WiFi)
    esp_err_t ret = nvs_flash_init();
    if (ret == ESP_ERR_NVS_NO_FREE_PAGES || ret == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        ESP_ERROR_CHECK(nvs_flash_erase());
        ret = nvs_flash_init();
    }
    ESP_ERROR_CHECK(ret);

    // 2. Power up the display
    gpio_config_t pwr_cfg = {.pin_bit_mask = BIT64(PIN_NUM_PWR), .mode = GPIO_MODE_OUTPUT};
    gpio_config(&pwr_cfg);
    gpio_set_level(PIN_NUM_PWR, 1);

    // 3. Turn on backlight
    gpio_config_t bk_cfg = {.pin_bit_mask = BIT64(PIN_NUM_BKLT), .mode = GPIO_MODE_OUTPUT};
    gpio_config(&bk_cfg);
    gpio_set_level(PIN_NUM_BKLT, 1);

    // 4. SPI bus
    spi_bus_config_t buscfg = {
        .sclk_io_num     = PIN_NUM_SCK,
        .mosi_io_num     = PIN_NUM_MOSI,
        .miso_io_num     = -1,
        .quadwp_io_num   = -1,
        .quadhd_io_num   = -1,
        .max_transfer_sz = 240 * 135 * sizeof(uint16_t),
    };
    ESP_ERROR_CHECK(spi_bus_initialize(LCD_HOST, &buscfg, SPI_DMA_CH_AUTO));

    // 5. Panel IO
    esp_lcd_panel_io_handle_t io_handle = NULL;
    esp_lcd_panel_io_spi_config_t io_config = {
        .dc_gpio_num      = PIN_NUM_DC,
        .cs_gpio_num      = PIN_NUM_CS,
        .pclk_hz          = 40 * 1000 * 1000,
        .lcd_cmd_bits     = 8,
        .lcd_param_bits   = 8,
        .spi_mode         = 0,
        .trans_queue_depth = 10,
    };
    ESP_ERROR_CHECK(esp_lcd_new_panel_io_spi((esp_lcd_spi_bus_handle_t)LCD_HOST, &io_config, &io_handle));

    // 6. ST7789 panel driver
    esp_lcd_panel_handle_t panel_handle = NULL;
    esp_lcd_panel_dev_config_t panel_config = {
        .reset_gpio_num  = PIN_NUM_RST,
        .rgb_ele_order   = LCD_RGB_ELEMENT_ORDER_RGB,
        .bits_per_pixel  = 16,
    };
    ESP_ERROR_CHECK(esp_lcd_new_panel_st7789(io_handle, &panel_config, &panel_handle));
    esp_lcd_panel_reset(panel_handle);
    esp_lcd_panel_init(panel_handle);
    // set_gap commented out for testing — tune once replacement board arrives
    // esp_lcd_panel_set_gap(panel_handle, 40, 52);
    esp_lcd_panel_invert_color(panel_handle, true);
    esp_lcd_panel_disp_on_off(panel_handle, true);

    // 7. LVGL port
    const lvgl_port_cfg_t lvgl_cfg = ESP_LVGL_PORT_INIT_CONFIG();
    ESP_ERROR_CHECK(lvgl_port_init(&lvgl_cfg));

    const lvgl_port_display_cfg_t disp_cfg = {
        .io_handle      = io_handle,
        .panel_handle   = panel_handle,
        .control_handle = NULL,
        .buffer_size    = 240 * 135,
        .double_buffer  = true,
        .trans_size     = 0,
        .hres           = 240,
        .vres           = 135,
        .rotation       = { .swap_xy = true, .mirror_x = false, .mirror_y = true },
        .color_format   = LV_COLOR_FORMAT_RGB565,
    };
    lv_display_t *disp = lvgl_port_add_disp(&disp_cfg);
    (void)disp;

    // 8. Draw solid red background (display test — replace in Phase 2)
    lvgl_port_lock(0);
    lv_obj_set_style_bg_color(lv_screen_active(), lv_color_hex(0xFF0000), 0);
    lv_obj_set_style_bg_opa(lv_screen_active(), LV_OPA_COVER, 0);
    lvgl_port_unlock();

    // 9. Connect to WiFi
    bool connected = wifi_init_sta();
    if (!connected) {
        ESP_LOGE(TAG, "WiFi failed — poll task will not start");
        // TODO: show error on display
        while (1) { vTaskDelay(pdMS_TO_TICKS(1000)); }
    }

    // 10. Start poll task
    xTaskCreate(poll_task, "poll_task", 8192, NULL, 5, NULL);

    // Keep app_main alive
    while (1) {
        vTaskDelay(pdMS_TO_TICKS(1000));
    }
}
