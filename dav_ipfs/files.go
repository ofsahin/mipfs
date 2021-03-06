package dav_ipfs

import (
	"encoding/xml"
	"github.com/ipfs/go-ipfs-api"
	"github.com/kpmy/mipfs/ipfs_api"
	"github.com/kpmy/ypk/fn"
	"github.com/mattetti/filebuffer"
	"io/ioutil"
	"os"
	"sync"
	"time"

	"fmt"
	"github.com/kpmy/ypk/dom"
	. "github.com/kpmy/ypk/tc"
	"golang.org/x/net/webdav"
	"io"
	"strconv"
	"strings"
)

type block struct {
	pos  int64
	data *filebuffer.Buffer
}

type file struct {
	ch    *chain
	pos   int64
	links []*shell.LsLink
	buf   *filebuffer.Buffer

	wr chan *block
	wg *sync.WaitGroup

	props dom.Element
}

func (f *file) Name() string {
	return f.ch.name
}

func (f *file) Size() int64 {
	return int64(f.ch.UnixLsObject.Size)
}

func (f *file) Mode() os.FileMode {
	return 0
}

func (f *file) ModTime() (ret time.Time) {
	ret = time.Now()
	if !fn.IsNil(f.props) {
		if ts := f.props.Attr("modified"); ts != "" {
			if sec, err := strconv.ParseInt(ts, 10, 64); err == nil {
				ret = time.Unix(sec, 0)
			}
		}
	}
	return
}

func (f *file) IsDir() bool {
	return false
}

func (f *file) Sys() interface{} {
	Halt(100)
	return nil
}

func (f *file) Read(p []byte) (n int, err error) {
	if f.links == nil {
		f.links, _ = ipfs_api.Shell().List(f.ch.Hash)
	}
	if len(f.links) == 0 {
		if fn.IsNil(f.buf) {
			f.buf = filebuffer.New(nil)
			rd, _ := ipfs_api.Shell().Cat(f.ch.Hash)
			io.Copy(f.buf, rd)
		}
		f.buf.Seek(f.pos, io.SeekStart)
		n, err = f.buf.Read(p)
		f.pos = f.pos + int64(n)
		return n, err
	} else {
		var end int64 = 0
		for _, l := range f.links {
			begin := end
			end = begin + int64(l.Size)
			if begin <= f.pos && f.pos < end {
				if f.buf == nil {
					rd, _ := ipfs_api.Shell().Cat(l.Hash)
					f.buf = filebuffer.New(nil)
					io.Copy(f.buf, rd)
					l.Size = uint64(f.buf.Buff.Len())
				}
				f.buf.Seek(f.pos-begin, io.SeekStart)
				n, err = f.buf.Read(p)
				f.pos = f.pos + int64(n)
				if f.buf.Index == int64(l.Size) {
					f.buf = nil
				}
				return
			}
		}
		panic(100)
	}
}

func (f *file) Seek(offset int64, whence int) (seek int64, err error) {
	switch whence {
	case io.SeekStart:
		f.pos = offset
	case io.SeekCurrent:
		f.pos = f.pos + offset
	case io.SeekEnd:
		f.pos = f.Size() + offset
	default:
		Halt(100)
	}
	Assert(f.pos >= 0, 60)
	seek = f.pos
	return
}

func (f *file) Readdir(count int) (ret []os.FileInfo, err error) {
	return nil, webdav.ErrForbidden
}

func (f *file) Stat() (os.FileInfo, error) {
	return f, nil
}

const emptyFileHash = "QmbFMke1KXqnYyBBWxB74N4c5SBnJMVAiMNRcGu6x1AwQH"

func (f *file) Close() error {
	if f.wr != nil {
		close(f.wr)
		f.wg.Wait()
	} else if !f.ch.exists() {
		f.update(nil)
	}
	return nil
}

func (f *file) update(data io.ReadCloser) {
	if !fn.IsNil(data) {
		f.ch.Hash, _ = ipfs_api.Shell().Add(data)
	} else {
		f.ch.Hash = emptyFileHash
	}
	for tail := f.ch.up; tail != nil; tail = tail.up {
		tail.Hash, _ = ipfs_api.Shell().PatchLink(tail.Hash, tail.down.name, tail.down.Hash, false)
		if tail.down.Hash == f.ch.Hash {
			//создадим пропы
			f.props = newPropsModel()
			f.props.Attr("modified", fmt.Sprint(time.Now().Unix()))
			propHash, _ := ipfs_api.Shell().Add(dom.EncodeWithHeader(f.props))
			tail.Hash, _ = ipfs_api.Shell().PatchLink(tail.Hash, "*"+f.ch.name, propHash, false)
		}
	}
	head := f.ch.head()
	head.link.update(head.Hash)
}

const BufferLimit = 1024 * 128

type ioFile interface {
	io.Seeker
	io.ReadCloser
	io.Writer
}

func (f *file) Write(p []byte) (n int, err error) {
	if f.wr == nil {
		f.wr = make(chan *block, 16)
		f.wg = new(sync.WaitGroup)
		f.wg.Add(1)
		go func(f *file) {
			var tmp ioFile
			buf := filebuffer.New(nil)
			tmp = buf
			for w := range f.wr {
				tmp.Seek(w.pos, io.SeekStart)
				w.data.Seek(0, io.SeekStart)
				io.Copy(tmp, w.data)
				if !fn.IsNil(buf) && buf.Buff.Len() > BufferLimit {
					tf, _ := ioutil.TempFile(os.TempDir(), "mipfs")
					buf.Seek(0, io.SeekStart)
					io.Copy(tf, buf)
					tmp = tf
					buf = nil
				}
			}
			tmp.Seek(0, io.SeekStart)
			f.update(tmp)
			f.wg.Done()
		}(f)
	}
	b := &block{pos: f.pos}
	b.data = filebuffer.New(nil)
	n, err = b.data.Write(p)
	f.wr <- b
	f.pos = f.pos + int64(n)
	return n, nil
}

func (f *file) readPropsModel() {
	if !strings.HasPrefix(f.ch.name, "*") {
		ls, _ := ipfs_api.Shell().FileList(f.ch.up.Hash)
		pm := propLinksMap(ls)
		if p, ok := pm[f.ch.name]; ok {
			rd, _ := ipfs_api.Shell().CacheCat(p.Hash)
			if el, err := dom.Decode(rd); err == nil {
				f.props = el.Model()
			} else {
				Halt(99, f.ch.name, f.ch.Hash)
			}
		} else {
			f.props = newPropsModel()
		}
	}
}

func (f *file) readPropsObject() (props map[xml.Name]dom.Element, err error) {
	props = make(map[xml.Name]dom.Element)
	f.readPropsModel()
	props = readProps(f.props)
	return
}

func (f *file) writePropsObject(props map[xml.Name]dom.Element) {
	if !strings.HasPrefix(f.ch.name, "*") {
		el := writeProps(props)
		propHash, _ := ipfs_api.Shell().Add(dom.EncodeWithHeader(el))
		for tail := f.ch.up; tail != nil; tail = tail.up {
			if tail.down.Hash == f.ch.Hash {
				tail.Hash, _ = ipfs_api.Shell().PatchLink(tail.Hash, "*"+f.ch.name, propHash, false)
			} else {
				tail.Hash, _ = ipfs_api.Shell().PatchLink(tail.Hash, tail.down.name, tail.down.Hash, false)
			}
		}
		head := f.ch.head()
		head.link.update(head.Hash)
	}
}

func (f *file) DeadProps() (ret map[xml.Name]webdav.Property, err error) {
	//log.Println("file prop get")
	pm, _ := f.readPropsObject()
	ret = props2webdav(pm)
	//log.Println("read file props", ret)
	return
}

func (f *file) Patch(patch []webdav.Proppatch) (ret []webdav.Propstat, err error) {
	//log.Println("file prop patch", patch)
	pe, _ := f.readPropsObject()
	ret = propsPatch(pe, patch)
	//log.Println("write file props", pe)
	f.writePropsObject(pe)
	return
}
