package sgtape

type PageDec struct {
	// std::uint16_t page_code;
	PageCode uint16
	// std::uint16_t length;
	Length uint16
	// std::byte flags;
	Flags byte
	// external data encryption control capable
	// static constexpr auto flags_extdecc_pos {2u};
	FlagsExtdeccPos byte // default as 2
	// static constexpr std::byte flags_extdecc_mask {3u << flags_extdecc_pos};
	FlagsExtdeccMask byte // default as 3 << 2
	// configuration prevented
	// static constexpr auto flags_cfg_p_pos {0u};
	FlagsCfgPPos byte // default as 0
	// static constexpr std::byte flags_cfg_p_mask {3u << flags_cfg_p_pos};
	FlagsCfgPMask byte // default as 3 << 0
	// std::byte reserved[15];
	Reserved [15]byte
	// algorithm_descriptor ads[];
	AlgorithmDescriptors []AlgorithmDescriptor
}

type AlgorithmDescriptor struct {
	// std::uint8_t algorithm_index;
	algorithm_index uint8
	// std::byte reserved1;
	reserved1 byte
	// std::uint16_t length;
	length uint16
	// std::byte flags1;
	flags1 byte
	// // algorithm valid for mounted volume
	// static constexpr auto flags1_avfmv_pos {7u};
	flags1_avfmv_pos byte // 7
	// static constexpr std::byte flags1_avfmv_mask {1u << flags1_avfmv_pos};
	flags1_avfmv_mask byte // 1 << 7
	// // supplemental decryption key capable
	// static constexpr auto flags1_sdk_c_pos {6u};
	flags1_sdk_c_pos byte // 6
	// static constexpr std::byte flags1_sdk_c_mask {1u << flags1_sdk_c_pos};
	flags1_sdk_c_mask byte // 1 << 6
	// // message authentication code capable
	// static constexpr auto flags1_mac_c_pos {5u};
	flags1_mac_c_pos byte // 5
	// static constexpr std::byte flags1_mac_c_mask {1u << flags1_mac_c_pos};
	flags1_mac_c_mask byte // 1 << 5
	// // distinguish encrypted logical block capable
	// static constexpr auto flags1_delb_c_pos {4u};
	flags1_delb_c_pos byte // 4
	// static constexpr std::byte flags1_delb_c_mask {1u << flags1_delb_c_pos};
	flags1_delb_c_mask byte // 1 < 4
	// // decryption capabilities
	// static constexpr auto flags1_decrypt_c_pos {2u};
	flags1_decrypt_c_pos byte // 2
	// static constexpr std::byte flags1_decrypt_c_mask {3u << flags1_decrypt_c_pos};
	flags1_decrypt_c_mask byte // 3 << 4
	// // encryption capabilities
	// static constexpr auto flags1_encrypt_c_pos {0u};
	flags1_encrypt_c_pos byte // 0
	// static constexpr std::byte flags1_encrypt_c_mask {3u << flags1_encrypt_c_pos};
	flags1_encrypt_c_mask byte // 3 << 0
	// std::byte flags2;
	flags2 byte
	// // algorithm valid for current logical position
	// static constexpr auto flags2_avfcp_pos {6u};
	flags2_avfcp_pos byte // 6
	// static constexpr std::byte flags2_avfcp_mask {3u << flags2_avfcp_pos};
	flags2_avfcp_mask byte // 3 << 6
	// // nonce capabilities
	// static constexpr auto flags2_nonce_pos {4u};
	flags2_nonce_pos byte // 4
	// static constexpr std::byte flags2_nonce_mask {3u << flags2_nonce_pos};
	flags2_nonce_mask byte // 3 << 4
	// // KAD format capable
	// static constexpr auto flags2_kadf_c_pos {3u};
	// static constexpr std::byte flags2_kadf_c_mask {1u << flags2_kadf_c_pos};
	// // volume contains encrypted logical blocks capable
	// static constexpr auto flags2_vcelb_c_pos {2u};
	// static constexpr std::byte flags2_vcelb_c_mask {1u << flags2_vcelb_c_pos};
	// // U-KAD fixed
	// static constexpr auto flags2_ukadf_pos {1u};
	// static constexpr std::byte flags2_ukadf_mask {1u << flags2_ukadf_pos};
	// // A-KAD fixed
	// static constexpr auto flags2_akadf_pos {0u};
	// static constexpr std::byte flags2_akadf_mask {1u << flags2_akadf_pos};
	// std::uint16_t maximum_ukad_length;
	// std::uint16_t maximum_akad_length;
	// std::uint16_t key_length;
	// std::byte flags3;
	// // decryption capabilities
	// static constexpr auto flags3_dkad_c_pos {6u};
	// static constexpr std::byte flags3_dkad_c_mask {3u << flags3_dkad_c_pos};
	// // external encryption mode control capabilities
	// static constexpr auto flags3_eemc_c_pos {4u};
	// static constexpr std::byte flags3_eemc_c_mask {3u << flags3_eemc_c_pos};
	// // raw decryption mode control capabilities
	// static constexpr auto flags3_rdmc_c_pos {1u};
	// static constexpr std::byte flags3_rdmc_c_mask {7u << flags3_rdmc_c_pos};
	// // encryption algorithm records encryption mode
	// static constexpr auto flags3_earem_pos {0u};
	// static constexpr std::byte flags3_earem_mask {1u << flags3_earem_pos};
	// std::uint8_t maximum_eedk_count;
	// static constexpr auto maximum_eedk_count_pos {0u};
	// static constexpr std::uint8_t maximum_eedk_count_mask {
	// 	15u << maximum_eedk_count_pos};
	// std::uint16_t msdk_count;
	// std::uint16_t maximum_eedk_size;
	// std::byte reserved2[2];
	// std::uint32_t security_algorithm_code;

	// static constexpr std::size_t header_size {4u};
}
