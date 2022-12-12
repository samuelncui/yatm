package sgtape

const (
	SSCCodeAllowOverwrite               = 0x82
	SSCCodeDisplayMessage               = 0xC0
	SSCCodeErase                        = 0x19
	SSCCodeFormatMedium                 = 0x04
	SSCCodeLoadUnload                   = 0x1B
	SSCCodeLocate10                     = 0x2B
	SSCCodeLocate16                     = 0x92
	SSCCodePreventAllowMediumRemoval    = 0x1E
	SSCCodeRead                         = 0x08
	SSCCodeReadBlockLimits              = 0x05
	SSCCodeReadDynamicRuntimeAttribute  = 0xD1
	SSCCodeReadPosition                 = 0x34
	SSCCodeReadReverse                  = 0x0F
	SSCCodeRecoverBufferedData          = 0x14
	SSCCodeReportDensitySupport         = 0x44
	SSCCodeRewind                       = 0x01
	SSCCodeSetCapacity                  = 0x0B
	SSCCodeSpace6                       = 0x11
	SSCCodeSpace16                      = 0x91
	SSCCodeStringSearch                 = 0xE3
	SSCCodeVerify                       = 0x13
	SSCCodeWrite                        = 0x0A
	SSCCodeWriteDynamicRuntimeAttribute = 0xD2
	SSCCodeWriteFilemarks6              = 0x10
)

const (
	SPCCodeChangeDefinition         = 0x40
	SPCCodeXcopy                    = 0x83
	SPCCodeInquiry                  = 0x12
	SPCCodeLogSelect                = 0x4C
	SPCCodeLogSense                 = 0x4D
	SPCCodeModeSelect6              = 0x15
	SPCCodeModeSelect10             = 0x55
	SPCCodeModeSense6               = 0x1A
	SPCCodeModeSense10              = 0x5A
	SPCCodePersistentReserveIn      = 0x5E
	SPCCodePersistentReserveOut     = 0x5F
	SPCCodeReadAttribute            = 0x8C
	SPCCodeReadBuffer               = 0x3C
	SPCCodeReceiveDiagnosticResults = 0x1C
	SPCCodeReleaseUnit6             = 0x17
	SPCCodeReleaseUnit10            = 0x57
	SPCCodeReportLuns               = 0xA0
	SPCCodeRequestSense             = 0x03
	SPCCodeReserveUnit6             = 0x16
	SPCCodeReserveUnit10            = 0x56
	SPCCodeSpin                     = 0xA2
	SPCCodeSpout                    = 0xB5
	SPCCodeSendDiagnostic           = 0x1D
	SPCCodeTestUnitReady            = 0x00
	SPCCodeWriteAttribute           = 0x8D
	SPCCodeWriteBuffer              = 0x3B
	SPCCodeThirdPartyCopyIn         = 0x84
	SPCCodeMaintenanceIn            = 0xA3
	SPCCodeMaintenanceOut           = 0xA4
)

// #define TEST_CRYPTO (0x20)
// #define MASK_CRYPTO (~0x20)

// typedef enum {
// 	TC_SPACE_EOD,   /* Space EOD          */
// 	TC_SPACE_FM_F,  /* Space FM Forward   */
// 	TC_SPACE_FM_B,  /* Space FM Backword  */
// 	TC_SPACE_F,     /* Space Rec Forward  */
// 	TC_SPACE_B,     /* Space Rec Backword */
// } TC_SPACE_TYPE;    /* Space command operations */

// typedef enum {
// 	TC_FORMAT_DEFAULT   = 0x00,   /* Make 1 partition medium */
// 	TC_FORMAT_PARTITION = 0x01,   /* Make 2 partition medium */
// 	TC_FORMAT_DEST_PART = 0x02,   /* Destroy all data and make 2 partition medium */
// 	TC_FORMAT_MAX       = 0x03
// } TC_FORMAT_TYPE;    /* Space command operations */

const (
	FormatDefault   = 0x00 // Make 1 partition medium
	FormatPartition = 0x01 // Make 2 partition medium
	FormatDestPart  = 0x02 // Destroy all data and make 2 partition medium
	FormatMax       = 0x03
)

// typedef enum {
// 	TC_MP_PC_CURRENT    = 0x00,    /* Get current value           */
// 	TC_MP_PC_CHANGEABLE = 0x40,    /* Get changeable bitmap       */
// 	TC_MP_PC_DEFAULT    = 0x80,    /* Get default(power-on) value */
// 	TC_MP_PC_SAVED      = 0xC0,    /* Get saved value             */
// } TC_MP_PC_TYPE;    /* Page control (PC) value for ModePage */

const (
	TC_MP_PC_CURRENT    = 0x00 /* Get current value           */
	TC_MP_PC_CHANGEABLE = 0x40 /* Get changeable bitmap       */
	TC_MP_PC_DEFAULT    = 0x80 /* Get default(power-on) value */
	TC_MP_PC_SAVED      = 0xC0 /* Get saved value             */
)

// #define TC_MP_DEV_CONFIG_EXT        (0x10) // ModePage 0x10 (Device Configuration Extension Page)
// #define TC_MP_SUB_DEV_CONFIG_EXT    (0x01) // ModePage SubPage 0x01 (Device Configuration Extension Page)
// #define TC_MP_DEV_CONFIG_EXT_SIZE   (48)

// #define TC_MP_CTRL                  (0x0A) // ModePage 0x0A (Control Page)
// #define TC_MP_SUB_DP_CTRL           (0xF0) // ModePage Subpage 0xF0 (Control Data Protection Page)
// #define TC_MP_SUB_DP_CTRL_SIZE      (48)

// #define TC_MP_COMPRESSION           (0x0F) // ModePage 0x0F (Data Compression Page)
// #define TC_MP_COMPRESSION_SIZE      (32)

// #define TC_MP_MEDIUM_PARTITION      (0x11) // ModePage 0x11 (Medium Partiton Page)
// #define TC_MP_MEDIUM_PARTITION_SIZE (28)

const (
	TC_MP_MEDIUM_PARTITION      = 0x11 // ModePage 0x11 (Medium Partiton Page)
	TC_MP_MEDIUM_PARTITION_SIZE = 28

	LOG_TAPECAPACITY      = 0x31
	LOG_TAPECAPACITY_SIZE = 32
)

// #define TC_MP_MEDIUM_SENSE          (0x23) // ModePage 0x23 (Medium Sense Page)
// #define TC_MP_MEDIUM_SENSE_SIZE     (76)

// #define TC_MP_INIT_EXT              (0x24) // ModePage 0x24 (Initator-Specific Extentions)
// #define TC_MP_INIT_EXT_SIZE         (40)

// #define TC_MP_READ_WRITE_CTRL       (0x25) // ModePage 0x25 (Read/Write Control Page)
// #define TC_MP_READ_WRITE_CTRL_SIZE  (48)

// #define TC_MP_SUPPORTEDPAGE         (0x3F) // ModePage 0x3F (Supported Page Info)
// #define TC_MP_SUPPORTEDPAGE_SIZE    (0xFF)

// #define TC_MAM_PAGE_HEADER_SIZE    (0x5)
// #define TC_MAM_PAGE_VCR            (0x0009) /* Page code of Volume Change Reference */
// #define TC_MAM_PAGE_VCR_SIZE       (0x4)    /* Size of Volume Change Reference */
// #define TC_MAM_PAGE_COHERENCY      (0x080C)
// #define TC_MAM_PAGE_COHERENCY_SIZE (0x46)

// #define TC_MAM_APP_VENDER          (0x0800)
// #define TC_MAM_APP_VENDER_SIZE     (0x8)
// #define TC_MAM_APP_NAME  (0x0801)
// #define TC_MAM_APP_NAME_SIZE (0x20)
// #define TC_MAM_APP_VERSION (0x0802)
// #define TC_MAM_APP_VERSION_SIZE (0x8)
// #define TC_MAM_USER_MEDIUM_LABEL (0x0803)
// #define TC_MAM_USER_MEDIUM_LABEL_SIZE (0xA0)
// #define TC_MAM_TEXT_LOCALIZATION_IDENTIFIER (0x0805)
// #define TC_MAM_TEXT_LOCALIZATION_IDENTIFIER_SIZE (0x1)
// #define TC_MAM_BARCODE (0x0806)
// #define TC_MAM_BARCODE_SIZE (0x20)
// #define TC_MAM_BARCODE_LEN TC_MAM_BARCODE_SIZE /* HPE LTFS alias */
// #define TC_MAM_MEDIA_POOL (0x0808)
// #define TC_MAM_MEDIA_POOL_SIZE (0xA0)
// #define TC_MAM_APP_FORMAT_VERSION (0x080B)
// #define TC_MAM_APP_FORMAT_VERSION_SIZE (0x10)
// #define TC_MAM_LOCKED_MAM (0x1623)
// #define TC_MAM_LOCKED_MAM_SIZE (0x1)

// #define BINARY_FORMAT (0x0)
// #define ASCII_FORMAT (0x1)
// #define TEXT_FORMAT (0x2)

// #define TEXT_LOCALIZATION_IDENTIFIER_ASCII (0x0)
// #define TEXT_LOCALIZATION_IDENTIFIER_UTF8 (0x81)

// #define TC_MAM_PAGE_ATTRIBUTE_ALL   0 /* Page code for all the attribute passed
// while formatting and mounting the volume */

// enum eod_status {
// 	EOD_GOOD        = 0x00,
// 	EOD_MISSING     = 0x01,
// 	EOD_UNKNOWN     = 0x02
// };

// enum {
// 	MEDIUM_UNKNOWN = 0,
// 	MEDIUM_PERFECT_MATCH,
// 	MEDIUM_WRITABLE,
// 	MEDIUM_PROBABLY_WRITABLE,
// 	MEDIUM_READONLY,
// 	MEDIUM_CANNOT_ACCESS
// };
