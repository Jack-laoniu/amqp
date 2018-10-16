package amqp

import (
	"fmt"
	"os"
	"time"

	"github.com/streadway/amqp"
)

type Client struct {
	dsn string
}

type Session struct {
	ch   *amqp.Channel
	conn *amqp.Connection
}

func Dial(dsn string) *Client {
	clt := &Client{
		dsn: dsn,
	}
	return clt
}

type Delivery struct {
	amqp.Delivery
}

func (d Delivery) GetBody() []byte {
	return d.Body
}

func (d Delivery) Accpet(flag bool) {
	if flag {
		d.Ack(false)
	} else {
		d.Nack(false, true)
	}
}

func (clt *Client) getSession() (*Session, error) {
	c, err := amqp.Dial(clt.dsn)
	if err != nil {
		return nil, err
	}
	ch, err := c.Channel()
	if err != nil {
		c.Close()
		c = nil
		return nil, err
	}
	return &Session{ch: ch, conn: c}, err
}

// AutoReconnecter 重连实现接口
// github.com/streadway/amqp 不自带重连 这里需要实现下
type AutoReconnecter interface {
	Reconnect() (*amqp.Channel, error)
}

type Sub struct {
	clt      *Client
	exchange string
	queue    string
	routing  string
	msgchan  chan interface{}
}

func (clt *Client) Sub(queue, exchange, routing string) *Sub {
	rev := &Sub{
		exchange: exchange,
		queue:    queue,
		clt:      clt,
		routing:  routing,
		msgchan:  make(chan interface{}),
	}
	go rev.reconnect()
	return rev
}

func (sub *Sub) reconnect() {
	for {
		sess, err := sub.clt.getSession()
		if err == nil {
			if err = sub.bind(sess.ch); err == nil {
				sess.conn.Close()
				continue
			}
		}
		if err != nil {
			sub.msgchan <- err
		}
		time.Sleep(time.Second * 2)
	}
}

func (sub *Sub) GetMessages() <-chan interface{} {
	return sub.msgchan
}

func (sub *Sub) bind(ch *amqp.Channel) error {
	defer ch.Close()
	// 声明队列，如果队列不存在则创建
	// 默认durable=true 持久化存储
	if _, err := ch.QueueDeclare(
		sub.queue, true, false, false, false, nil,
	); err != nil {
		return err
	}
	// 绑定队列到交换机 以便从指定交换机获取数据
	if err := ch.QueueBind(
		sub.queue, sub.routing, sub.exchange, false, nil,
	); err != nil {
		return err
	}
	msgs, err := ch.Consume(sub.queue, os.Args[0], false, false, false, false, nil)
	if err != nil {
		return err
	}
loop:
	for {
		select {
		case msg, ok := <-msgs:
			if !ok {
				break loop
			}
			sub.msgchan <- &Delivery{msg}
		case <-time.After(time.Second * 30):
			// 超过30秒收不到任何消息 重新连接下
			// 因为exchange被删或者其他并不会触发 导致一直获取不到消息
			break
		}
	}
	return nil
}

type Pub struct {
	clt      *Client
	session  chan *Session
	exchange string
	kind     string
}

func (clt *Client) Pub(exchange, kind string) (*Pub, error) {
	maxidle := 3
	pub := &Pub{
		clt:      clt,
		session:  make(chan *Session, maxidle),
		exchange: exchange,
		kind:     kind,
	}
	for i := 0; i < maxidle; i++ {
		s, err := pub.reconnect()
		if err != nil {
			return nil, err
		}
		pub.putSession(s)
	}
	return pub, nil
}

func (pub *Pub) reconnect() (*Session, error) {
	sess, err := pub.clt.getSession()
	if err != nil {
		return nil, err
	}
	if err = sess.ch.ExchangeDeclare(
		pub.exchange, pub.kind, true, false, false, false, nil); err != nil {
		sess.conn.Close()
		sess = nil
	}
	return sess, err
}

func (pub *Pub) Push(routing string, data []byte) error {
	for {
		select {
		case s := <-pub.session:
			// 一直取session 直到取不到
			// 就从超时里重新获取 还是失败返回错误
			err := pub.pushaction(s, routing, data)
			if err != nil {
				s.ch.Close()
				s.conn.Close()
				continue
			}
			pub.putSession(s)
			return nil
		case <-time.After(time.Second * 2):
			s, err := pub.reconnect()
			if err != nil {
				return err
			}
			if err := pub.pushaction(s, routing, data); err != nil {
				s.ch.Close()
				s.conn.Close()
				return err
			}
			pub.putSession(s)
		}
	}
}

func (pub *Pub) putSession(s *Session) {
	select {
	case pub.session <- s:
	default:
		s.ch.Close()
		s.conn.Close()
		fmt.Println("full")
	}
}

func (pub *Pub) pushaction(s *Session, routing string, data []byte) error {
	return s.ch.Publish(
		pub.exchange, routing, false, false,
		amqp.Publishing{
			Headers:      amqp.Table{},
			Body:         data,
			DeliveryMode: amqp.Persistent, // 1=non-persistent, 2=persistent
			Priority:     0,               // 0-9
		},
	)
}
