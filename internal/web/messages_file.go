package web

import (
	"fmt"

	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

// fileMessagesID is the Indonesian of every file-rule code, keyed
// "file."+code, as fmt formats taking the error's Args in order. The line
// is never part of a sentence: "file.line" puts it in front. English needs
// no entry; it is the error's own Msg.
var fileMessagesID = map[string]string{
	"file.line": "baris %[1]d: %[2]s",

	"file.title_missing":          `baris pertama harus berisi "# Title"`,
	"file.title_too_long":         `judul lebih panjang dari %d karakter`,
	"file.header_expected":        `seharusnya ada "shuffle:", "show_result:", "monitor:", "duration:", judul bagian "## " atau soal 1`,
	"file.section_late":           `judul bagian pertama harus ada sebelum soal 1`,
	"file.section_title_empty":    `judul bagian kosong`,
	"file.section_title_too_long": `judul bagian lebih panjang dari %d karakter`,
	"file.too_many_sections":      `ujian berisi lebih dari %d bagian`,
	"file.question_number":        `seharusnya soal %[1]d, tetapi yang ditemukan soal %[2]d`,
	"file.too_many_questions":     `ujian berisi lebih dari %d soal`,
	"file.fence_open":             `blok kode tidak pernah ditutup`,
	"file.no_questions":           `ujian tidak berisi soal`,
	"file.section_empty":          `bagian %d tidak berisi soal`,
	"file.header_duplicate":       `pengaturan "%s" ditulis lebih dari sekali`,
	"file.shuffle_value":          `shuffle harus "none", "questions" atau "questions, options"`,
	"file.show_result_value":      `show_result harus "none", "score" atau "review"`,
	"file.monitor_value":          `monitor harus "on" atau "off"`,
	"file.duration_value":         `duration harus bilangan bulat dari %[1]d sampai %[2]d menit`,
	"file.question_no_text":       `soal %d tidak berisi teks`,
	"file.question_both_kinds":    `soal %d punya pilihan sekaligus jawaban yang diterima; gunakan baris "-" atau baris "=", jangan keduanya`,
	"file.question_no_answers":    `soal %d perlu pilihan (baris yang diawali "-") atau jawaban yang diterima (baris yang diawali "=")`,
	"file.question_few_options":   `soal %d perlu paling sedikit dua pilihan`,
	"file.question_many_options":  `soal %[1]d punya lebih dari %[2]d pilihan`,
	"file.option_no_text":         `soal %[1]d, pilihan %[2]d tidak berisi teks`,
	"file.question_no_correct":    `soal %d tidak punya pilihan yang benar`,
	"file.question_many_correct":  `soal %d punya lebih dari satu pilihan yang benar`,
	"file.question_many_accepted": `soal %[1]d punya lebih dari %[2]d jawaban yang diterima`,
	"file.accepted_no_text":       `soal %[1]d, jawaban yang diterima %[2]d tidak berisi teks`,
	"file.accepted_multiline":     `soal %[1]d, jawaban yang diterima %[2]d harus satu baris`,
	"file.accepted_too_long":      `soal %[1]d, jawaban yang diterima %[2]d lebih panjang dari %[3]d karakter`,
	"file.image_outside_question": `soal %[1]d: gambar "%[2]s" harus berupa file di dalam unggahan`,
	"file.image_outside_section":  `bagian %[1]d: gambar "%[2]s" harus berupa file di dalam unggahan`,
	"file.image_missing_question": `soal %[1]d: gambar "%[2]s" tidak ditemukan di unggahan`,
	"file.image_missing_section":  `bagian %[1]d: gambar "%[2]s" tidak ditemukan di unggahan`,
	"file.image_type_question":    `soal %[1]d: gambar "%[2]s" bukan file PNG, JPEG, GIF atau WebP`,
	"file.image_type_section":     `bagian %[1]d: gambar "%[2]s" bukan file PNG, JPEG, GIF atau WebP`,
	"file.upload_empty":           `unggahan kosong`,
	"file.zip_unreadable":         `file zip tidak bisa dibaca`,
	"file.zip_unsafe_path":        `zip berisi jalur file yang tidak aman`,
	"file.zip_name_not_utf8":      `zip berisi nama file yang bukan UTF-8 yang benar`,
	"file.zip_too_many_files":     `zip berisi lebih dari %d file`,
	"file.zip_entry_too_large":    `file "%s" di dalam zip lebih besar dari 5 MB`,
	"file.zip_total_too_large":    `total isi zip lebih besar dari 20 MB`,
	"file.zip_text_count":         `zip harus berisi tepat satu file ujian .md atau .txt, ditemukan %d`,
	"file.text_too_large":         `teks ujian lebih besar dari 1 MB`,
	"file.text_not_utf8":          `teks ujian bukan UTF-8 yang benar`,

	"file.editor_title_line":         `judul harus satu baris teks`,
	"file.editor_answer_line":        `jawaban yang diterima harus satu baris teks`,
	"file.editor_section_title_line": `judul bagian harus satu baris teks`,
	"file.editor_too_large":          `ujian beserta gambarnya lebih besar dari 10 MB, ukuran file terbesar yang bisa diimpor`,
	"file.editor_not_storable":       `teks ini tidak bisa disimpan di file ujian`,
	"file.editor_image_size":         `gambar lebih besar dari 5 MB`,
	"file.editor_image_type":         `gambar bukan file PNG, JPEG, GIF atau WebP`,
	"file.editor_image_hidden":       `baris gambar tidak akan tampil di akhir teks, misalnya setelah HTML mentah; ubah akhir teks lalu simpan lagi`,
	"file.editor_image_name_taken":   `ujian ini sudah punya file gambar lain bernama %s`,
	"file.editor_shape":              `soal %d: teks tidak bisa disimpan seperti yang ditulis: baris yang diawali "-" akan menjadi pilihan, dan pilihan yang diakhiri "*" akan menjadi pilihan yang benar; tulis \- atau \* sebagai gantinya`,
	"file.editor_title_missing":      `ujian perlu judul`,
	"file.editor_first_section":      `bagian pertama harus dimulai dari soal 1`,
	"file.editor_section_left_empty": `bagian %d akan kosong tanpa soal; hapus dulu bagian itu`,
}

// FileError is e's sentence in l: Msg in English or for a code without a
// translation, else the Indonesian with e's values and line.
func (l localizer) FileError(e *examfile.Error) string {
	text, ok := fileMessagesID["file."+string(e.Code)]
	if l.lang != langID || !ok {
		return e.Msg
	}
	if len(e.Args) > 0 {
		text = fmt.Sprintf(text, e.Args...)
	}
	if e.Line > 0 {
		return fmt.Sprintf(fileMessagesID["file.line"], e.Line, text)
	}
	return text
}
